package pack

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/buildpack/lifecycle"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockercli "github.com/docker/docker/client"
	"github.com/google/uuid"
)

func Build(appDir, detectImage, repoName string, publish bool) (err error) {
	buildFlags := &BuildFlags{
		AppDir:      appDir,
		DetectImage: detectImage,
		RepoName:    repoName,
		Publish:     publish,
	}
	buildFlags.Cli, err = dockercli.NewEnvClient()
	if err != nil {
		return err
	}
	return buildFlags.Run()
}

type BuildFlags struct {
	AppDir      string
	DetectImage string
	RepoName    string
	Publish     bool
	Cli         *dockercli.Client
}

func (b *BuildFlags) Run() error {
	var err error
	b.AppDir, err = filepath.Abs(b.AppDir)
	if err != nil {
		return err
	}

	uid := uuid.New().String()
	launchVolume := fmt.Sprintf("pack-launch-%x", uid)
	workspaceVolume := fmt.Sprintf("pack-workspace-%x", uid)
	cacheVolume := fmt.Sprintf("pack-cache-%x", md5.Sum([]byte(b.AppDir)))
	// defer exec.Command("docker", "volume", "rm", "-f", launchVolume).Run()
	// defer exec.Command("docker", "volume", "rm", "-f", workspaceVolume).Run()

	// fmt.Println("*** COPY APP TO VOLUME:")
	if err := copyToVolume(b.DetectImage, launchVolume, b.AppDir, "app"); err != nil {
		return err
	}

	fmt.Println("*** DETECTING:")
	if err := b.Detect(uid, launchVolume, workspaceVolume); err != nil {
		return err
	}

	group, err := groupToml(workspaceVolume, b.DetectImage)
	if err != nil {
		return err
	}

	fmt.Println("*** ANALYZING: Reading information from previous image for possible re-use")
	if b.Publish {
		cmd := exec.Command("docker", "run",
			"--rm",
			"-v", launchVolume+":/launch",
			"-v", workspaceVolume+":/workspace",
			// TODO below line assumes too many things
			"-v", filepath.Join(os.Getenv("HOME"), ".docker")+":/home/packs/.docker:ro",
			"-e", "PACK_USE_HELPERS=true",
			"dgodd/packsv3:analyze",
			b.RepoName,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	} else {
		txt, err := exec.Command("docker", "inspect", b.RepoName, "-f", `{{index .Config.Labels "sh.packs.build"}}`).Output()
		// fmt.Println(string(txt), err)
		if err == nil && len(txt) > 0 {
			cmd := exec.Command(
				"docker", "run",
				"-i", // so that stdin works
				"--rm",
				"-v", launchVolume+":/launch",
				"-v", workspaceVolume+":/workspace",
				"dgodd/packsv3:analyze",
				"-metadata-on-stdin",
				b.RepoName,
			)
			cmd.Stdin = bytes.NewReader(txt)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Run(); err != nil {
				return err
			}
		} else {
			fmt.Println("    No previous image foound")
		}
	}

	fmt.Println("*** BUILDING:")
	cmd := exec.Command("docker", "run",
		"--rm",
		"-v", launchVolume+":/launch",
		"-v", workspaceVolume+":/workspace",
		"-v", cacheVolume+":/cache",
		group.BuildImage,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	if !b.Publish {
		fmt.Println("*** PULLING RUN IMAGE LOCALLY:")
		if out, err := exec.Command("docker", "pull", group.RunImage).CombinedOutput(); err != nil {
			fmt.Println(string(out))
			return err
		}
	}

	fmt.Println("*** EXPORTING:")
	if b.Publish {
		cmd = exec.Command("docker", "run",
			"--rm",
			"-v", launchVolume+":/launch",
			"-v", workspaceVolume+":/workspace", // TODO I think this can be deleted
			// TODO below line assumes too many things
			"-v", filepath.Join(os.Getenv("HOME"), ".docker")+":/home/packs/.docker:ro",
			"-e", "PACK_USE_HELPERS=true",
			"-e", "PACK_RUN_IMAGE="+group.RunImage,
			"dgodd/packsv3:export",
			b.RepoName,
		)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	} else {
		fullStart := time.Now()
		start := time.Now()
		var localLaunchDir string
		cleanup := func() {}
		if !b.Publish {
			localLaunchDir, cleanup, err = exportVolume(b.DetectImage, launchVolume)
			if err != nil {
				return err
			}

			fmt.Printf("    copy '/launch' to host: %s\n", time.Since(start))
			start = time.Now()
		}
		defer cleanup()

		_, err = dockerBuildExport(group, localLaunchDir, b.RepoName, group.RunImage)
		if err != nil {
			return err
		}
		fmt.Printf("    create image: %s (%s)\n", time.Since(start), time.Since(fullStart))
	}

	return nil
}

func (b *BuildFlags) Detect(uid, launchVolume, workspaceVolume string) error {
	ctx := context.Background()
	ctr, err := b.Cli.ContainerCreate(ctx, &container.Config{
		Image: b.DetectImage,
		// Entrypoint:   []string{},
		// Cmd:          []string{"find", "/launch"},
	}, &container.HostConfig{
		Binds: []string{
			launchVolume + ":/launch",
			workspaceVolume + ":/workspace",
		},
	}, nil, "pack-detect-"+uid)
	if err != nil {
		return err
	}
	defer b.Cli.ContainerRemove(context.Background(), ctr.ID, dockertypes.ContainerRemoveOptions{Force: true})

	if err := b.runContainer(ctx, ctr.ID); err != nil {
		return err
	}
	return nil
}
func exportVolume(image, volName string) (string, func(), error) {
	tmpDir, err := ioutil.TempDir("", "pack.build.")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	containerName := uuid.New().String()
	if output, err := exec.Command("docker", "container", "create", "--name", containerName, "-v", volName+":/launch:ro", image).CombinedOutput(); err != nil {
		cleanup()
		fmt.Println(string(output))
		return "", func() {}, err
	}
	defer exec.Command("docker", "rm", containerName).Run()
	if output, err := exec.Command("docker", "cp", containerName+":/launch/.", tmpDir).CombinedOutput(); err != nil {
		cleanup()
		fmt.Println(string(output))
		return "", func() {}, err
	}

	return tmpDir, cleanup, nil
}

func copyToVolume(image, volName, srcDir, destDir string) error {
	containerName := uuid.New().String()
	if output, err := exec.Command("docker", "container", "create", "--user", "0", "--name", containerName, "--entrypoint", "", "-v", volName+":/launch", image, "chown", "-R", "packs:packs", "/launch").CombinedOutput(); err != nil {
		fmt.Println(string(output))
		return err
	}
	defer exec.Command("docker", "rm", containerName).Run()
	if output, err := exec.Command("docker", "cp", srcDir+"/.", containerName+":"+filepath.Join("/launch", destDir)).CombinedOutput(); err != nil {
		fmt.Println(string(output))
		return err
	}

	if output, err := exec.Command("docker", "start", containerName).CombinedOutput(); err != nil {
		fmt.Println(string(output))
		return err
	}
	return nil
}

func groupToml(workspaceVolume, detectImage string) (lifecycle.BuildpackGroup, error) {
	var buf bytes.Buffer
	cmd := exec.Command("docker", "run", "--rm", "-v", workspaceVolume+":/workspace:ro", "--entrypoint", "", detectImage, "bash", "-c", "cat $PACK_BP_GROUP_PATH")
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return lifecycle.BuildpackGroup{}, err
	}

	var group lifecycle.BuildpackGroup
	if _, err := toml.Decode(buf.String(), &group); err != nil {
		return lifecycle.BuildpackGroup{}, err
	}

	return group, nil
}

func (b *BuildFlags) runContainer(ctx context.Context, id string) error {
	if err := b.Cli.ContainerStart(ctx, id, dockertypes.ContainerStartOptions{}); err != nil {
		return err
	}
	out, err := b.Cli.ContainerLogs(ctx, id, dockertypes.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return err
	}
	go func() {
		io.Copy(os.Stdout, out)
	}()
	waitC, errC := b.Cli.ContainerWait(ctx, id, "")
	select {
	case w := <-waitC:
		if w.StatusCode != 0 {
			return fmt.Errorf("container run: non zero exit: %d: %s", w.StatusCode, w.Error)
		}
	case err := <-errC:
		return err
	}
	return nil
}
