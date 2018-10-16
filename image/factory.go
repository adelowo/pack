package image

import (
	"context"
	"io"
	"log"

	"github.com/buildpack/lifecycle/img"
	"github.com/buildpack/pack/fs"
	"github.com/buildpack/packs"
	"github.com/docker/docker/api/types"
	"github.com/google/go-containerregistry/pkg/v1"
)

type Image2 interface {
	Label(string) (string, error)
	Name() string
	Rebase(string, Image2) error
	SetLabel(string, string) error
	TopLayer() (string, error)
	Save() (string, error)
}

type Docker interface {
	PullImage(ref string) error
	ImageInspectWithRaw(ctx context.Context, imageID string) (types.ImageInspect, []byte, error)
	ImageBuild(ctx context.Context, buildContext io.Reader, options types.ImageBuildOptions) (types.ImageBuildResponse, error)
	ImageRemove(ctx context.Context, ref string, options types.ImageRemoveOptions) ([]types.ImageDeleteResponseItem, error)
}

type Factory struct {
	Docker Docker
	Log    *log.Logger
	Stdout io.Writer
	FS     *fs.FS
}

type Client struct{}

func (c *Client) ReadImage(repoName string, useDaemon bool) (v1.Image, error) {
	repoStore, err := c.RepoStore(repoName, useDaemon)
	if err != nil {
		return nil, err
	}

	origImage, err := repoStore.Image()
	if err != nil {
		// Assume error is due to non-existent image
		return nil, nil
	}
	if _, err := origImage.RawManifest(); err != nil {
		// Assume error is due to non-existent image
		// This is necessary for registries
		return nil, nil
	}

	return origImage, nil
}

func (c *Client) RepoStore(repoName string, useDaemon bool) (img.Store, error) {
	newRepoStore := img.NewRegistry
	if useDaemon {
		newRepoStore = img.NewDaemon
	}
	repoStore, err := newRepoStore(repoName)
	if err != nil {
		return nil, packs.FailErr(err, "access", repoName)
	}
	return repoStore, nil
}
