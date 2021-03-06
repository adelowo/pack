package pack

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/buildpack/pack/logging"
	"github.com/buildpack/pack/style"

	"github.com/buildpack/lifecycle/image"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/buildpack/pack/config"
	"github.com/buildpack/pack/docker"
	"github.com/buildpack/pack/fs"

	"github.com/BurntSushi/toml"
	"github.com/buildpack/lifecycle"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/pkg/errors"
)

type BuildFactory struct {
	Cli          Docker
	Logger       *logging.Logger
	FS           FS
	Config       *config.Config
	ImageFactory ImageFactory
}

type BuildFlags struct {
	AppDir     string
	Builder    string
	RunImage   string
	EnvFile    string
	RepoName   string
	Publish    bool
	NoPull     bool
	ClearCache bool
	Buildpacks []string
}

type BuildConfig struct {
	AppDir     string
	Builder    string
	RunImage   string
	EnvFile    map[string]string
	RepoName   string
	Publish    bool
	NoPull     bool
	ClearCache bool
	Buildpacks []string
	// Above are copied from BuildFlags are set by init
	Cli    Docker
	Logger *logging.Logger
	FS     FS
	Config *config.Config
	// Above are copied from BuildFactory
	CacheVolume string
}

const (
	launchDir     = "/workspace"
	buildpacksDir = "/buildpacks"
	platformDir   = "/platform"
	orderPath     = "/buildpacks/order.toml"
	groupPath     = `/workspace/group.toml`
	planPath      = "/workspace/plan.toml"
)

func DefaultBuildFactory(logger *logging.Logger) (*BuildFactory, error) {
	f := &BuildFactory{
		Logger: logger,
		FS:     &fs.FS{},
	}

	var err error
	f.Cli, err = docker.New()
	if err != nil {
		return nil, err
	}

	f.Config, err = config.NewDefault()
	if err != nil {
		return nil, err
	}

	f.ImageFactory, err = image.DefaultFactory()
	if err != nil {
		return nil, err
	}

	return f, nil
}

func (bf *BuildFactory) BuildConfigFromFlags(f *BuildFlags) (*BuildConfig, error) {
	if f.AppDir == "" {
		var err error
		f.AppDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
		bf.Logger.Verbose("Defaulting app directory to current working directory %s (use --path to override)", style.Symbol(f.AppDir))
	}
	appDir, err := filepath.Abs(f.AppDir)
	if err != nil {
		return nil, err
	}

	if f.RepoName == "" {
		f.RepoName = fmt.Sprintf("pack.local/run/%x", md5.Sum([]byte(appDir)))
	}

	b := &BuildConfig{
		AppDir:     appDir,
		RepoName:   f.RepoName,
		Publish:    f.Publish,
		NoPull:     f.NoPull,
		ClearCache: f.ClearCache,
		Buildpacks: f.Buildpacks,
		Cli:        bf.Cli,
		Logger:     bf.Logger,
		FS:         bf.FS,
		Config:     bf.Config,
	}

	if f.EnvFile != "" {
		b.EnvFile, err = parseEnvFile(f.EnvFile)
		if err != nil {
			return nil, err
		}
	}

	if f.Builder == "" {
		bf.Logger.Verbose("Using default builder image %s", style.Symbol(bf.Config.DefaultBuilder))
		b.Builder = bf.Config.DefaultBuilder
	} else {
		bf.Logger.Verbose("Using user-provided builder image %s", style.Symbol(f.Builder))
		b.Builder = f.Builder
	}
	if !f.NoPull {
		bf.Logger.Verbose("Pulling builder image %s (use --no-pull flag to skip this step)", style.Symbol(b.Builder))
	}

	builderImage, err := bf.ImageFactory.NewLocal(b.Builder, !f.NoPull)
	if err != nil {
		return nil, err
	}

	builderStackID, err := builderImage.Label("io.buildpacks.stack.id")
	if err != nil {
		return nil, fmt.Errorf("invalid builder image %s: %s", style.Symbol(b.Builder), err)
	}
	if builderStackID == "" {
		return nil, fmt.Errorf("invalid builder image %s: missing required label %s", style.Symbol(b.Builder), style.Symbol("io.buildpacks.stack.id"))
	}
	stack, err := bf.Config.Get(builderStackID)
	if err != nil {
		return nil, err
	}

	if f.RunImage != "" {
		bf.Logger.Verbose("Using user-provided run image %s", style.Symbol(f.RunImage))
		b.RunImage = f.RunImage
	} else {
		reg, err := config.Registry(f.RepoName)
		if err != nil {
			return nil, err
		}
		b.RunImage, err = config.ImageByRegistry(reg, stack.RunImages)
		if err != nil {
			return nil, err
		}
		b.Logger.Verbose("Selected run image %s from stack %s", style.Symbol(b.RunImage), style.Symbol(builderStackID))
	}

	var runImage image.Image
	if f.Publish {
		runImage, err = bf.ImageFactory.NewRemote(b.RunImage)
		if err != nil {
			return nil, err
		}
	} else {
		if !f.NoPull {
			bf.Logger.Verbose("Pulling run image %s (use --no-pull flag to skip this step)", style.Symbol(b.RunImage))
		}
		runImage, err = bf.ImageFactory.NewLocal(b.RunImage, !f.NoPull)
		if err != nil {
			return nil, err
		}
	}

	if runStackID, err := runImage.Label("io.buildpacks.stack.id"); err != nil {
		return nil, fmt.Errorf("invalid run image %s: %s", style.Symbol(b.RunImage), err)
	} else if runStackID == "" {
		return nil, fmt.Errorf("invalid run image %s: missing required label %s", style.Symbol(b.RunImage), style.Symbol("io.buildpacks.stack.id"))
	} else if builderStackID != runStackID {
		return nil, fmt.Errorf("invalid stack: stack %s from run image %s does not match stack %s from builder image %s", style.Symbol(runStackID), style.Symbol(b.RunImage), style.Symbol(builderStackID), style.Symbol(b.Builder))
	}

	b.CacheVolume, err = CacheVolume(f.RepoName)
	if err != nil {
		return nil, err
	}
	bf.Logger.Verbose(fmt.Sprintf("Using cache volume %s", style.Symbol(b.CacheVolume)))

	return b, nil
}

// TODO: This function has no tests! Also, should it take a `BuildFlags` object instead of all these args?
func Build(logger *logging.Logger, appDir, buildImage, runImage, repoName string, publish, clearCache bool) error {
	bf, err := DefaultBuildFactory(logger)
	if err != nil {
		return err
	}
	b, err := bf.BuildConfigFromFlags(&BuildFlags{
		AppDir:     appDir,
		Builder:    buildImage,
		RunImage:   runImage,
		RepoName:   repoName,
		Publish:    publish,
		ClearCache: clearCache,
	})
	if err != nil {
		return err
	}
	return b.Run()
}

func (b *BuildConfig) Run() error {
	if err := b.Detect(); err != nil {
		return err
	}

	b.Logger.Verbose(style.Step("ANALYZING"))
	b.Logger.Verbose("Reading information from previous image for possible re-use")
	if err := b.Analyze(); err != nil {
		return err
	}

	b.Logger.Verbose(style.Step("BUILDING"))
	if err := b.Build(); err != nil {
		return err
	}

	b.Logger.Verbose(style.Step("EXPORTING"))
	if err := b.Export(); err != nil {
		return err
	}

	return nil
}

func (b *BuildConfig) parseBuildpack(ref string) (string, string) {
	parts := strings.Split(ref, "@")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	b.Logger.Verbose("No version for %s buildpack provided, will use %s", style.Symbol(parts[0]), style.Symbol(parts[0]+"@latest"))
	return parts[0], "latest"
}

func (b *BuildConfig) copyBuildpacksToContainer(ctx context.Context, ctrID string) ([]*lifecycle.Buildpack, error) {
	var buildpacks []*lifecycle.Buildpack
	for _, bp := range b.Buildpacks {
		var id, version string
		if _, err := os.Stat(filepath.Join(bp, "buildpack.toml")); !os.IsNotExist(err) {
			if runtime.GOOS == "windows" {
				return nil, fmt.Errorf("directory buildpacks are not implemented on windows")
			}
			var buildpackTOML struct {
				Buildpack Buildpack
			}

			_, err = toml.DecodeFile(filepath.Join(bp, "buildpack.toml"), &buildpackTOML)
			if err != nil {
				return nil, fmt.Errorf(`failed to decode buildpack.toml from "%s": %s`, bp, err)
			}
			id = buildpackTOML.Buildpack.ID
			version = buildpackTOML.Buildpack.Version
			bpDir := filepath.Join(buildpacksDir, buildpackTOML.Buildpack.escapedID(), version)
			ftr, errChan := b.FS.CreateTarReader(bp, bpDir, 0, 0)
			if err := b.Cli.CopyToContainer(ctx, ctrID, "/", ftr, dockertypes.CopyToContainerOptions{}); err != nil {
				return nil, errors.Wrapf(err, "copying buildpack '%s' to container", bp)
			}
			if err := <-errChan; err != nil {
				return nil, errors.Wrapf(err, "copying buildpack '%s' to container", bp)
			}
		} else {
			id, version = b.parseBuildpack(bp)
		}
		buildpacks = append(
			buildpacks,
			&lifecycle.Buildpack{ID: id, Version: version, Optional: false},
		)
	}
	return buildpacks, nil
}

func (b *BuildConfig) Detect() error {
	ctx := context.Background()

	if b.ClearCache {
		if err := b.Cli.VolumeRemove(ctx, b.CacheVolume, true); err != nil {
			return errors.Wrap(err, "clearing cache")
		}
		b.Logger.Verbose("Cache volume %s cleared", style.Symbol(b.CacheVolume))
	}

	ctr, err := b.Cli.ContainerCreate(ctx, &container.Config{
		Image: b.Builder,
		Cmd: []string{
			"/lifecycle/detector",
			"-buildpacks", buildpacksDir,
			"-order", orderPath,
			"-group", groupPath,
			"-plan", planPath,
		},
	}, &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:%s:", b.CacheVolume, launchDir),
		},
	}, nil, "")
	if err != nil {
		return errors.Wrap(err, "container create")
	}
	defer b.Cli.ContainerRemove(ctx, ctr.ID, dockertypes.ContainerRemoveOptions{})

	var orderToml string
	b.Logger.Verbose(style.Step("DETECTING"))
	if len(b.Buildpacks) == 0 {
		orderToml = "" // use order.toml already in image
	} else {
		b.Logger.Verbose("Using manually-provided group")

		buildpacks, err := b.copyBuildpacksToContainer(ctx, ctr.ID)
		if err != nil {
			return errors.Wrap(err, "copy buildpacks to container")
		}

		groups := lifecycle.BuildpackOrder{
			lifecycle.BuildpackGroup{
				Buildpacks: buildpacks,
			},
		}

		var tomlBuilder strings.Builder
		if err := toml.NewEncoder(&tomlBuilder).Encode(map[string]interface{}{"groups": groups}); err != nil {
			return errors.Wrapf(err, "encoding order.toml: %#v", groups)
		}

		orderToml = tomlBuilder.String()
	}

	tr, errChan := b.FS.CreateTarReader(b.AppDir, launchDir+"/app", 0, 0)
	if err := b.Cli.CopyToContainer(ctx, ctr.ID, "/", tr, dockertypes.CopyToContainerOptions{}); err != nil {
		return errors.Wrap(err, "copy app to workspace volume")
	}

	if err := <-errChan; err != nil {
		return errors.Wrap(err, "copy app to workspace volume")
	}

	uid, gid, err := b.packUidGid(b.Builder)
	if err != nil {
		return errors.Wrap(err, "get pack uid gid")
	}
	if err := b.chownDir(launchDir+"/app", uid, gid); err != nil {
		return errors.Wrap(err, "chown app to workspace volume")
	}

	if orderToml != "" {
		ftr, err := b.FS.CreateSingleFileTar(orderPath, orderToml)
		if err != nil {
			return errors.Wrap(err, "converting order TOML to tar reader")
		}
		if err := b.Cli.CopyToContainer(ctx, ctr.ID, "/", ftr, dockertypes.CopyToContainerOptions{}); err != nil {
			return errors.Wrap(err, fmt.Sprintf("creating %s", orderPath))
		}
	}

	if err := b.copyEnvsToContainer(ctx, ctr.ID); err != nil {
		return err
	}

	if err := b.Cli.RunContainer(
		ctx,
		ctr.ID,
		b.Logger.VerboseWriter().WithPrefix("detector"),
		b.Logger.VerboseErrorWriter().WithPrefix("detector"),
	); err != nil {
		return errors.Wrap(err, "run detect container")
	}
	return nil
}

func (b *BuildConfig) Analyze() error {
	ctx := context.Background()
	ctrConf := &container.Config{
		Image: b.Builder,
	}
	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:%s:", b.CacheVolume, launchDir),
		},
	}

	if b.Publish {
		authHeader, err := authHeader(b.RepoName)
		if err != nil {
			return err
		}

		ctrConf.Env = []string{fmt.Sprintf(`PACK_REGISTRY_AUTH=%s`, authHeader)}
		ctrConf.Cmd = []string{
			"/lifecycle/analyzer",
			"-layers", launchDir,
			"-group", groupPath,
			b.RepoName,
		}
		hostConfig.NetworkMode = "host"
	} else {
		ctrConf.Cmd = []string{
			"/lifecycle/analyzer",
			"-layers", launchDir,
			"-group", groupPath,
			"-daemon",
			b.RepoName,
		}
		ctrConf.User = "root"
		hostConfig.Binds = append(hostConfig.Binds, "/var/run/docker.sock:/var/run/docker.sock")
	}

	ctr, err := b.Cli.ContainerCreate(ctx, ctrConf, hostConfig, nil, "")
	if err != nil {
		return errors.Wrap(err, "analyze container create")
	}
	defer b.Cli.ContainerRemove(ctx, ctr.ID, dockertypes.ContainerRemoveOptions{})

	if err := b.Cli.RunContainer(
		ctx,
		ctr.ID,
		b.Logger.VerboseWriter().WithPrefix("analyzer"),
		b.Logger.VerboseErrorWriter().WithPrefix("analyzer"),
	); err != nil {
		return errors.Wrap(err, "analyze run container")
	}

	uid, gid, err := b.packUidGid(b.Builder)
	if err != nil {
		return errors.Wrap(err, "get pack uid and gid")
	}
	if err := b.chownDir(launchDir, uid, gid); err != nil {
		return errors.Wrap(err, "chown launch dir")
	}

	return nil
}

func authHeader(repoName string) (string, error) {
	r, err := name.ParseReference(repoName, name.WeakValidation)
	if err != nil {
		return "", err
	}
	auth, err := authn.DefaultKeychain.Resolve(r.Context().Registry)
	if err != nil {
		return "", err
	}
	return auth.Authorization()
}

func (b *BuildConfig) Build() error {
	ctx := context.Background()
	ctr, err := b.Cli.ContainerCreate(ctx, &container.Config{
		Image: b.Builder,
		Cmd: []string{
			"/lifecycle/builder",
			"-buildpacks", buildpacksDir,
			"-layers", launchDir,
			"-group", groupPath,
			"-plan", planPath,
			"-platform", platformDir,
		},
	}, &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:%s:", b.CacheVolume, launchDir),
		},
	}, nil, "")
	if err != nil {
		return errors.Wrap(err, "build container create")
	}
	defer b.Cli.ContainerRemove(ctx, ctr.ID, dockertypes.ContainerRemoveOptions{})

	if len(b.Buildpacks) > 0 {
		_, err = b.copyBuildpacksToContainer(ctx, ctr.ID)
		if err != nil {
			return errors.Wrap(err, "copy buildpacks to container")
		}
	}

	if err := b.copyEnvsToContainer(ctx, ctr.ID); err != nil {
		return err
	}

	if err = b.Cli.RunContainer(
		ctx,
		ctr.ID,
		b.Logger.VerboseWriter().WithPrefix("builder"),
		b.Logger.VerboseErrorWriter().WithPrefix("builder"),
	); err != nil {
		return errors.Wrap(err, "running builder in container")
	}
	return nil
}

func parseEnvFile(envFile string) (map[string]string, error) {
	out := make(map[string]string, 0)
	f, err := ioutil.ReadFile(envFile)
	if err != nil {
		return nil, errors.Wrapf(err, "open %s", envFile)
	}
	for _, line := range strings.Split(string(f), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		arr := strings.SplitN(line, "=", 2)
		if len(arr) > 1 {
			out[arr[0]] = arr[1]
		} else {
			out[arr[0]] = os.Getenv(arr[0])
		}
	}
	return out, nil
}

func (b *BuildConfig) tarEnvFile() (io.Reader, error) {
	now := time.Now()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for k, v := range b.EnvFile {
		if err := tw.WriteHeader(&tar.Header{Name: "/platform/env/" + k, Size: int64(len(v)), Mode: 0444, ModTime: now}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(v)); err != nil {
			return nil, err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: "/platform/env/", Mode: 0555, ModTime: now}); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

func (b *BuildConfig) copyEnvsToContainer(ctx context.Context, containerID string) error {
	if len(b.EnvFile) > 0 {
		platformEnvTar, err := b.tarEnvFile()
		if err != nil {
			return errors.Wrap(err, "create env files")
		}
		if err := b.Cli.CopyToContainer(ctx, containerID, "/", platformEnvTar, dockertypes.CopyToContainerOptions{}); err != nil {
			return errors.Wrap(err, "create env files")
		}
	}
	return nil
}

func (b *BuildConfig) Export() error {
	ctx := context.Background()
	ctrConf := &container.Config{
		Image: b.Builder,
	}
	hostConfig := &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:%s:", b.CacheVolume, launchDir),
		},
	}

	if b.Publish {
		authHeader, err := authHeader(b.RepoName)
		if err != nil {
			return err
		}

		ctrConf.Env = []string{fmt.Sprintf(`PACK_REGISTRY_AUTH=%s`, authHeader)}
		ctrConf.Cmd = []string{
			"/lifecycle/exporter",
			"-image", b.RunImage,
			"-layers", launchDir,
			"-group", groupPath,
			b.RepoName,
		}
		hostConfig.NetworkMode = "host"
	} else {
		ctrConf.Cmd = []string{
			"/lifecycle/exporter",
			"-image", b.RunImage,
			"-layers", launchDir,
			"-group", groupPath,
			"-daemon",
			b.RepoName,
		}
		ctrConf.User = "root"
		hostConfig.Binds = append(hostConfig.Binds, "/var/run/docker.sock:/var/run/docker.sock")
	}

	ctr, err := b.Cli.ContainerCreate(ctx, ctrConf, hostConfig, nil, "")
	if err != nil {
		return errors.Wrap(err, "create export container")
	}
	defer b.Cli.ContainerRemove(ctx, ctr.ID, dockertypes.ContainerRemoveOptions{})

	uid, gid, err := b.packUidGid(b.Builder)
	if err != nil {
		return errors.Wrap(err, "get pack uid and gid")
	}
	if err := b.chownDir(launchDir, uid, gid); err != nil {
		return errors.Wrap(err, "chown launch dir")
	}

	if err := b.Cli.RunContainer(
		ctx,
		ctr.ID,
		b.Logger.VerboseWriter().WithPrefix("exporter"),
		b.Logger.VerboseErrorWriter().WithPrefix("exporter"),
	); err != nil {
		return errors.Wrap(err, "run lifecycle/exporter")
	}
	return nil
}

func (b *BuildConfig) packUidGid(builder string) (int, int, error) {
	i, _, err := b.Cli.ImageInspectWithRaw(context.Background(), builder)
	if err != nil {
		return 0, 0, errors.Wrap(err, "reading builder env variables")
	}
	var sUID, sGID string
	for _, kv := range i.Config.Env {
		kv2 := strings.SplitN(kv, "=", 2)
		if len(kv2) == 2 && kv2[0] == "PACK_USER_ID" {
			sUID = kv2[1]
		} else if len(kv2) == 2 && kv2[0] == "PACK_GROUP_ID" {
			sGID = kv2[1]
		}
	}
	if sUID == "" || sGID == "" {
		return 0, 0, errors.New("not found pack uid & gid")
	}
	var uid, gid int
	uid, err = strconv.Atoi(sUID)
	if err != nil {
		return 0, 0, errors.Wrapf(err, "parsing pack uid: %s", sUID)
	}
	gid, err = strconv.Atoi(sGID)
	if err != nil {
		return 0, 0, errors.Wrapf(err, "parsing pack gid: %s", sGID)
	}
	return uid, gid, nil
}

func (b *BuildConfig) chownDir(path string, uid, gid int) error {
	ctx := context.Background()
	ctr, err := b.Cli.ContainerCreate(ctx, &container.Config{
		Image: b.Builder,
		Cmd:   []string{"chown", "-R", fmt.Sprintf("%d:%d", uid, gid), path},
		User:  "root",
	}, &container.HostConfig{
		Binds: []string{
			fmt.Sprintf("%s:%s:", b.CacheVolume, launchDir),
		},
	}, nil, "")
	if err != nil {
		return err
	}
	defer b.Cli.ContainerRemove(ctx, ctr.ID, dockertypes.ContainerRemoveOptions{})
	if err := b.Cli.RunContainer(ctx, ctr.ID, b.Logger.VerboseWriter(), b.Logger.VerboseErrorWriter()); err != nil {
		return err
	}
	return nil
}
