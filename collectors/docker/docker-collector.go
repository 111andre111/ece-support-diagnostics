package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/elastic/beats/libbeat/logp"
	"github.com/elastic/ece-support-diagnostics/collectors/zookeeper"
	"github.com/elastic/ece-support-diagnostics/config"
	"github.com/elastic/ece-support-diagnostics/helpers"
	"github.com/elastic/ece-support-diagnostics/store"
)

var re = regexp.MustCompile(`\/f[r|a]c-(\w+(?:-\w+)?)-(\w+)`)

type fileSystemStore struct {
	store.ContentStore
	cfg *config.Config
}

func Run(t store.ContentStore, config *config.Config) {
	store := fileSystemStore{t, config}
	store.runDockerCmds()
}

func (t fileSystemStore) runDockerCmds() {
	l := logp.NewLogger("docker")
	// log := logp.NewLogger("docker")
	dockerMsg := "Collecting Docker information"
	l.Infof(dockerMsg)
	fmt.Println("[ ] " + dockerMsg)

	const defaultDockerAPIVersion = "v1.23"

	cli, err := client.NewClientWithOpts(client.WithVersion(defaultDockerAPIVersion))
	if err != nil {
		panic(err)
	}
	// cli.NegotiateAPIVersion(context.Background())
	l.Infof("Using Docker API Version: %s", cli.ClientVersion())

	ctx := context.Background()
	Containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		// Should cancel here if can't reach docker API.
		panic(err)
	}

	fp := func(path string) string { return filepath.Join(t.cfg.DiagnosticFilename(), "server_info", path) }

	t.writeJSON(fp("DockerContainers.json"), Containers)
	// writeJSON(fp("DockerContainers.json"), cmd(Containers, err), tar)

	imageList, _ := cli.ImageList(ctx, types.ImageListOptions{})
	t.writeJSON(fp("DockerRepository.json"), imageList)
	// writeJSON(fp("DockerRepository.json"), cmd(cli.ImageList(ctx, types.ImageListOptions{})), tar)

	info, _ := cli.Info(ctx)
	t.writeJSON(fp("DockerInfo.json"), info)
	// writeJSON(fp("DockerInfo.json"), cmd(cli.Info(ctx)), tar)

	diskUsage, _ := cli.DiskUsage(ctx)
	t.writeJSON(fp("DockerDiskUsage.json"), diskUsage)
	// writeJSON(fp("DockerDiskUsage.json"), cmd(cli.DiskUsage(ctx)), tar)

	serverVersion, _ := cli.ServerVersion(ctx)
	t.writeJSON(fp("DockerServerVersion.json"), serverVersion)
	// writeJSON(fp("DockerServerVersion.json"), cmd(cli.ServerVersion(ctx)), tar)

	helpers.ClearStdoutLine()
	fmt.Println("[✔] Collected Docker information")

	since := t.cfg.StartTime.Add(-t.cfg.OlderThan).Format(time.RFC3339Nano)
	l.Infof("Docker will ignore log entries older than %s", since)

	for _, container := range Containers {
		// if container.Names[0] == "/frc-cloud-uis-cloud-ui" {
		// 	if cfg.DisableRest != true {
		// 		runRest(tar)
		// 	}
		// }

		// https://github.com/elastic/ece-support-diagnostics/issues/5
		if container.Names[0] == "/frc-zookeeper-servers-zookeeper" {
			zookeeper.Run(t, container, t.cfg)
			// zookeeperMNTR(container, tar)
			fmt.Println("[✔] Collected Zookeeper data")
		}
	}

	fmt.Println("[ ] Collecting Docker logs")
	for _, container := range Containers {
		t.dockerLogs(cli, container, since)
	}
	helpers.ClearStdoutLine()
	fmt.Println("[✔] Collected Docker logs")
}

func (t fileSystemStore) dockerLogs(cli *client.Client, container types.Container, since string) {
	l := logp.NewLogger("docker_logs")

	// getValue := func(key string) string { if val, ok := container.Labels[key]; ok {return val}; return "" }
	dockerName := container.Names[0]

	if strings.HasPrefix(dockerName, "/frc") || strings.HasPrefix(dockerName, "/fac") {
		filePath := t.createFilePath(container)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		logOptions := types.ContainerLogsOptions{
			Since:      since,
			ShowStdout: true,
			ShowStderr: true,
		}

		l.Infof("Writing logs for container: %.12s", container.ID)
		reader, err := cli.ContainerLogs(ctx, container.ID, logOptions)
		if err != nil {
			l.Error(err)
		}

		// Demultiplex stdout and stderror
		// from the container logs
		stdoutput := new(bytes.Buffer)
		stderror := new(bytes.Buffer)

		stdcopy.StdCopy(stdoutput, stderror, reader)
		if err != nil {
			panic(err)
		}

		// Need to evaluate how to stream bytes to tar file, rather than copy all bytes?
		stdout, _ := ioutil.ReadAll(stdoutput)
		// ignore empty data
		if len(stdout) > 0 {
			t.AddData(filePath+".stdout.log", stdout)
		}
		stderr, _ := ioutil.ReadAll(stderror)
		// ignore empty data
		if len(stderr) > 0 {
			t.AddData(filePath+".stderr.log", stderr)
		}

		// //read the first 8 bytes to ignore the HEADER part from docker container logs
		// p := make([]byte, 8)
		// reader.Read(p)

		// logData, err := ioutil.ReadAll(reader)
		// if err != nil && err != io.EOF {
		// 	logp.Error(err)
		// }
		// tar.AddData(filePath+".log", logData)

		cTop, err := cli.ContainerTop(ctx, container.ID, []string{})
		cTjson, err := json.MarshalIndent(cTop, "", "  ")
		// err = ioutil.WriteFile(filePath+".top", cTjson, 0644)
		t.AddData(filePath+".top", cTjson)

		if err != nil {
			panic(err)
		}
	}

}

// TODO: FIX THIS, regex should be passed in, not global
// func createFilePath(container types.Container, re *regexp.Regexp) string {
func (t fileSystemStore) createFilePath(container types.Container) string {
	dockerName := container.Names[0]
	labels := container.Labels

	eceLogPath := func(kind, filename string) string {
		return filepath.Join(t.cfg.DiagnosticFilename(), "ece", kind, filename)
	}
	// if a runner launches the container then it has `runner.container_name`
	if containerName, ok := labels["co.elastic.cloud.runner.container_name"]; ok {
		version := strings.Split(labels["org.label-schema.version"], "-")[0] // "2.1.0-SNAPSHOT"
		fileName := fmt.Sprintf("%s_%s_%.12s", containerName, version, container.ID)

		return eceLogPath(containerName, fileName)
		// cloud-ui_2.1.0_0b3a6993d552.log
	}

	// if an allocator launches the container then it as `allocator.kind`
	if kind, ok := labels["co.elastic.cloud.allocator.kind"]; ok {
		// "elasticsearch" | "kibana"
		clusterID := labels["co.elastic.cloud.allocator.cluster_id"]     // "c5900a8affb44d108ebe31513480a9b8"
		version := labels["co.elastic.cloud.allocator.type_version"]     // "6.6.0"
		instanceName := labels["co.elastic.cloud.allocator.instance_id"] // "instance-0000000000"
		fileName := fmt.Sprintf("%.12s_%s-%s", container.ID, kind, version+".log")

		return filepath.Join(t.cfg.DiagnosticFilename(), kind, clusterID, instanceName, fileName)
		// 5a4f7f_elasticsearch-5.6.14_instance-0000000000_506b8c016045.log
	}

	// these should be special containers that auto start themselves on reboot
	//  thus they do not have any of the docker Labels above
	//  this also serves as a catch all
	var name string
	match := re.FindStringSubmatch(dockerName)
	if len(match) == 3 {
		name = match[2]
	} else {
		name = strings.TrimPrefix(dockerName, "/frc-")
	}
	version := strings.Split(container.Labels["org.label-schema.version"], "-")[0]
	fileName := fmt.Sprintf("%s_%s_%.12s", name, version, container.ID)
	return eceLogPath(name, fileName)

}

func (t fileSystemStore) writeJSON(path string, apiResp interface{}) error {
	json, err := json.MarshalIndent(apiResp, "", "  ")
	if err != nil {
		panic(err)
	}
	err = t.AddData(path, json)
	if err != nil {
		panic(err)
	}
	return err
}

// func writeJSON(path string, apiResp interface{}, tar *Tarball) error {
// 	json, err := json.MarshalIndent(apiResp, "", "  ")
// 	if err != nil {
// 		panic(err)
// 	}
// 	err = tar.AddData(path, json)
// 	if err != nil {
// 		panic(err)
// 	}
// 	return err
// }

// func safeFilename(names ...string) string {
// 	// TODO: Make sure this is actually file system safe
// 	// This should be reworked into something that validates the full fs path.
// 	filename := ""
// 	r := strings.NewReplacer(
// 		"docker.elastic.co", "",
// 		"\\", "_",
// 		"/", "_",
// 		":", "_",
// 		"*", "_",
// 		"?", "_",
// 		"\"", "_",
// 		"<", "_",
// 		">", "_",
// 		"|", "_",
// 		".", "_",
// 	)
// 	size := len(names)
// 	for i, name := range names {
// 		if i == size || i == 0 {
// 			filename = r.Replace(name)
// 		} else {
// 			filename = filename + "__" + r.Replace(name)
// 		}
// 	}
// 	return filename
// }

// // Hack to allow calling writeJson directly
// func cmd(api interface{}, err error) interface{} {
// 	return api
// }
