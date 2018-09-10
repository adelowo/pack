package pack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"

	"github.com/buildpack/lifecycle"
	"github.com/buildpack/packs"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
)

func (b *BuildFlags) topLayerForImage(image string) (string, string, error) {
	ctx := context.Background()
	i, _, err := b.Cli.ImageInspectWithRaw(ctx, image)
	if err != nil {
		return "", "", err
	}
	return i.ID, i.RootFS.Layers[len(i.RootFS.Layers)-1], nil
}

func (b *BuildFlags) dockerBuildExport(group *lifecycle.BuildpackGroup, launchVolume string, launchData LaunchData, repoName, stackName string) (string, error) {
	ctx := context.Background()
	image, stackTopLayer, err := b.topLayerForImage(stackName)
	if err != nil {
		return "", err
	}
	metadata := packs.BuildMetadata{
		RunImage: packs.RunImageMetadata{
			Name: stackName,
			SHA:  stackTopLayer,
		},
		App:        packs.AppMetadata{},
		Config:     packs.ConfigMetadata{},
		Buildpacks: []packs.BuildpackMetadata{},
	}

	mvDir := func(image, name string) (string, string, error) {
		ctr, err := b.Cli.ContainerCreate(ctx, &container.Config{
			Image:      image,
			User:       "root",
			Entrypoint: []string{},
			Cmd:        []string{"bash", "-c", fmt.Sprintf(`mkdir -p "$(dirname /launch/%s)" && mv "/launch-volume/%s" "/launch/%s" && chown -R packs:packs "/launch/"`, name, name, name)},
		}, &container.HostConfig{
			Binds: []string{
				launchVolume + ":/launch-volume",
			},
		}, nil, "")
		if err != nil {
			return "", "", err
		}
		defer b.Cli.ContainerRemove(context.Background(), ctr.ID, dockertypes.ContainerRemoveOptions{Force: true})
		if err := b.runContainer(ctx, ctr.ID, os.Stdout, os.Stderr); err != nil {
			return "", "", err
		}
		res, err := b.Cli.ContainerCommit(ctx, ctr.ID, dockertypes.ContainerCommitOptions{})
		if err != nil {
			return "", "", err
		}
		// fmt.Println("ADD LAYER:", res.ID)
		return b.topLayerForImage(res.ID)
	}

	var topLayer string
	fmt.Println("    add dir: app")
	image, topLayer, err = mvDir(image, "app")
	if err != nil {
		return "", err
	}
	metadata.App.SHA = topLayer

	fmt.Println("    add dir: config")
	image, topLayer, err = mvDir(image, "config")
	if err != nil {
		return "", err
	}
	metadata.Config.SHA = topLayer

	for _, buildpack := range group.Buildpacks {
		layers := make(map[string]packs.LayerMetadata)
		for _, tomlFile := range launchData.Files[buildpack.ID] {
			name := strings.TrimSuffix(tomlFile, ".toml")
			dir := buildpack.ID + "/" + name
			if launchData.Dirs[dir] {
				fmt.Println("    add dir:", dir)
				image, topLayer, err = mvDir(image, dir)
				if err != nil {
					return "", err
				}
			} else {
				fmt.Println("    add dir from prev image:", dir)
				dockerfile := fmt.Sprintf("FROM %s AS prev\n\nFROM %s\nCOPY --from=prev --chown=packs:packs /launch/%s /launch/%s\n", repoName, image, dir, dir)
				image, err = dockerBuild(b.Cli, dockerfile, ioutil.Discard)
				if err != nil {
					return "", err
				}
				image, topLayer, err = b.topLayerForImage(image)
				if err != nil {
					return "", err
				}
			}
			layers[name] = packs.LayerMetadata{
				SHA:  topLayer,
				Data: launchData.Data[buildpack.ID+"/"+tomlFile],
			}
		}
		metadata.Buildpacks = append(metadata.Buildpacks, packs.BuildpackMetadata{
			Key:    buildpack.ID,
			Layers: layers,
		})
	}

	shPacksBuild, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	image, err = b.addLabelToImage(image, map[string]string{"sh.packs.build": string(shPacksBuild)})
	if err != nil {
		return "", err
	}

	if err := b.Cli.ImageTag(ctx, image, repoName); err != nil {
		return "", err
	}
	return image, nil
}

func (b *BuildFlags) addLabelToImage(image string, labels map[string]string) (string, error) {
	ctx := context.Background()
	ctr, err := b.Cli.ContainerCreate(ctx, &container.Config{
		Image:  image,
		Labels: labels,
	}, nil, nil, "")
	if err != nil {
		return "", err
	}
	defer b.Cli.ContainerRemove(context.Background(), ctr.ID, dockertypes.ContainerRemoveOptions{Force: true})
	res, err := b.Cli.ContainerCommit(ctx, ctr.ID, dockertypes.ContainerCommitOptions{})
	if err != nil {
		return "", err
	}
	return res.ID, nil
}

func (b *BuildFlags) dockerBuild(dockerfile string) (string, string, error) {
	tr, err := singleFileTar("Dockerfile", dockerfile)
	if err != nil {
		return "", "", err
	}
	res, err := b.Cli.ImageBuild(context.Background(), tr, dockertypes.ImageBuildOptions{})
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()

	jr := json.NewDecoder(res.Body)
	var id string
	var out struct {
		Stream string `json:"stream"`
		Aux    struct {
			ID string `json:"ID"`
		} `json:"aux"`
	}
	for {
		err := jr.Decode(&out)
		if err != nil {
			if err == io.EOF {
				break
			}
			panic(err)
		}
		if out.Aux.ID != "" {
			id = out.Aux.ID
		}
		if txt := strings.TrimSpace(out.Stream); txt != "" {
			fmt.Println(txt)
		}
	}

	return b.topLayerForImage(id)
}
