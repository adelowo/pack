package pack

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/buildpack/lifecycle"
	"github.com/buildpack/pack/config"
	"github.com/buildpack/packs/img"
	"github.com/google/go-containerregistry/pkg/v1"
	"github.com/pkg/errors"
)

type BuilderConfig struct {
	Repo       img.Store
	Buildpacks []Buildpack                `toml:"buildpacks"`
	Groups     []lifecycle.BuildpackGroup `toml:"groups"`
	BaseImage  v1.Image
}

type Buildpack struct {
	ID  string
	URI string
}

//go:generate mockgen -package mocks -destination mocks/docker.go github.com/buildpack/pack Docker
type Docker interface {
	PullImage(ref string) error
}

//go:generate mockgen -package mocks -destination mocks/images.go github.com/buildpack/pack Images
type Images interface {
	ReadImage(repoName string, useDaemon bool) (v1.Image, error)
	RepoStore(repoName string, useDaemon bool) (img.Store, error)
}

type BuilderFactory struct {
	Log    *log.Logger
	Docker Docker
	FS     FS
	Config *config.Config
	Images Images
}

//go:generate mockgen -package mocks -destination mocks/fs.go github.com/buildpack/pack FS
type FS interface {
	CreateTGZFile(tarFile, srcDir, tarDir string, uid, gid int) error
	CreateTarReader(srcDir, tarDir string, uid, gid int) (io.Reader, chan error)
	Untar(r io.Reader, dest string) error
	CreateSingleFileTar(path, txt string) (io.Reader, error)
}

type CreateBuilderFlags struct {
	RepoName        string
	BuilderTomlPath string
	StackID         string
	Publish         bool
	NoPull          bool
}

func (f *BuilderFactory) BuilderConfigFromFlags(flags CreateBuilderFlags) (BuilderConfig, error) {
	baseImage, err := f.baseImageName(flags.StackID)
	if err != nil {
		return BuilderConfig{}, err
	}
	if !flags.NoPull && !flags.Publish {
		f.Log.Println("Pulling builder base image ", baseImage)
		err := f.Docker.PullImage(baseImage)
		if err != nil {
			return BuilderConfig{}, fmt.Errorf(`failed to pull stack build image "%s": %s`, baseImage, err)
		}
	}
	var builderConfig BuilderConfig
	_, err = toml.DecodeFile(flags.BuilderTomlPath, &builderConfig)
	if err != nil {
		return BuilderConfig{}, fmt.Errorf(`failed to decode builder config from file "%s": %s`, flags.BuilderTomlPath, err)
	}
	builderConfig.BaseImage, err = f.Images.ReadImage(baseImage, !flags.Publish)
	if err != nil {
		return BuilderConfig{}, fmt.Errorf(`failed to read base image "%s": %s`, baseImage, err)
	}
	if builderConfig.BaseImage == nil {
		return BuilderConfig{}, fmt.Errorf(`base image "%s" was not found`, baseImage)
	}
	builderConfig.Repo, err = f.Images.RepoStore(flags.RepoName, !flags.Publish)
	if err != nil {
		return BuilderConfig{}, fmt.Errorf(`failed to create repository store for builder image "%s": %s`, flags.RepoName, err)
	}
	return builderConfig, nil
}

func (f *BuilderFactory) baseImageName(stackID string) (string, error) {
	if stackID == "" {
		stackID = f.Config.DefaultStackID
	}
	for _, stack := range f.Config.Stacks {
		if stack.ID == stackID {
			if len(stack.BuildImages) < 1 {
				return "", fmt.Errorf(`Invalid stack: stack "%s" requies at least one build image`, stackID)
			}
			return stack.BuildImages[0], nil
		}
	}
	return "", fmt.Errorf(`Missing stack: stack with id "%s" not found in pack config.toml`, stackID)
}

func (f *BuilderFactory) Create(config BuilderConfig) error {
	tmpDir, err := ioutil.TempDir("", "create-builder")
	if err != nil {
		return fmt.Errorf(`failed to create temporary directory: %s`, err)
	}
	defer os.Remove(tmpDir)

	orderTar, err := f.orderLayer(tmpDir, config.Groups)
	if err != nil {
		return fmt.Errorf(`failed generate order.toml layer: %s`, err)
	}
	builderImage, _, err := img.Append(config.BaseImage, orderTar)
	if err != nil {
		return fmt.Errorf(`failed append order.toml layer to image: %s`, err)
	}
	for _, buildpack := range config.Buildpacks {
		tarFile, err := f.buildpackLayer(tmpDir, buildpack)
		if err != nil {
			return fmt.Errorf(`failed generate layer for buildpack "%s": %s`, buildpack.ID, err)
		}
		builderImage, _, err = img.Append(builderImage, tarFile)
		if err != nil {
			return fmt.Errorf(`failed append buildpack layer to image: %s`, err)
		}
	}

	return config.Repo.Write(builderImage)
}

type order struct {
	Groups []lifecycle.BuildpackGroup `toml:"groups"`
}

func (f *BuilderFactory) orderLayer(dest string, groups []lifecycle.BuildpackGroup) (layerTar string, err error) {
	buildpackDir := filepath.Join(dest, "buildpack")
	err = os.Mkdir(buildpackDir, 0755)
	if err != nil {
		return "", err
	}

	orderFile, err := os.Create(filepath.Join(buildpackDir, "order.toml"))
	if err != nil {
		return "", err
	}
	defer orderFile.Close()
	err = toml.NewEncoder(orderFile).Encode(order{Groups: groups})
	if err != nil {
		return "", err
	}
	layerTar = filepath.Join(dest, "order.tar")
	if err := f.FS.CreateTGZFile(layerTar, buildpackDir, "/buildpacks", 0, 0); err != nil {
		return "", err
	}
	return layerTar, nil
}

func (f *BuilderFactory) buildpackLayer(dest string, buildpack Buildpack) (layerTar string, err error) {
	dir := strings.TrimPrefix(buildpack.URI, "file://")
	var data struct {
		BP struct {
			ID      string `toml:"id"`
			Version string `toml:"version"`
		} `toml:"buildpack"`
	}
	_, err = toml.DecodeFile(filepath.Join(dir, "buildpack.toml"), &data)
	if err != nil {
		return "", errors.Wrapf(err, "reading buildpack.toml from buildpack: %s", filepath.Join(dir, "buildpack.toml"))
	}
	bp := data.BP
	if buildpack.ID != bp.ID {
		return "", fmt.Errorf("buildpack ids did not match: %s != %s", buildpack.ID, bp.ID)
	}
	if bp.Version == "" {
		return "", fmt.Errorf("buildpack.toml must provide version: %s", filepath.Join(dir, "buildpack.toml"))
	}
	tarFile := filepath.Join(dest, fmt.Sprintf("%s.%s.tar", buildpack.ID, bp.Version))
	if err := f.FS.CreateTGZFile(tarFile, dir, filepath.Join("/buildpacks", buildpack.ID, bp.Version), 0, 0); err != nil {
		return "", err
	}
	return tarFile, err
}
