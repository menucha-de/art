package containers

import (
	"github.com/containerd/containerd"
	"github.com/menucha-de/utils"
)

const SystemdClient = "systemd:8080"

type Client struct {
	utils.Client
	CClient *containerd.Client
}
