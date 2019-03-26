package ecediag

import (
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/elastic/beats/libbeat/logp"
)

// zookeeperMNTR sends `echo mntr|nc ip port` for zookeeper
//  could not use localhost or 0.0.0.0, the response gets dropped
//  discovers the zookeep docker port between 2100-2199
//  then sends the command to ipv4 address of the docker gateway
func zookeeperMNTR(container types.Container, tar *Tarball) {
	log := logp.NewLogger("zookeeper")
	log.Info("Collecting zookeeper mntr")

	var port uint16
	for _, p := range container.Ports {
		if p.PublicPort >= 2100 && p.PublicPort <= 2199 {
			port = p.PublicPort
		}
	}
	if port == 0 {
		log.Error("Could not determine Zookeeper port")
		return
	}

	ip := container.NetworkSettings.Networks["bridge"].Gateway

	portString := fmt.Sprintf("%d", port)
	cmd := exec.Command("nc", ip, portString)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		defer stdin.Close()
		io.WriteString(stdin, "mntr")
	}()

	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("It didn't work:\n%s\n%s", err, out)
	}

	fpath := filepath.Join(cfg.DiagName, "ece", "zookeeper_mntr.txt")
	tar.AddData(fpath, out)
	// fmt.Println(test, err)
}
