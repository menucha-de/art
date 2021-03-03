/*
 * App Lifecycle Service
 *
 * This files describes the app lifecycle service
 *
 * API version: 1.0.0
 * Contact: opensource@peramic.io
 */
package swagger

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/peramic/App.Containerd/go/containers"
	"github.com/peramic/logging"
	loglib "github.com/peramic/logging"
	utils "github.com/peramic/utils"
)

var log *loglib.Logger = loglib.GetLogger("art")
var client containers.Client

const SystemdClient = "systemd:8080"
const dateTimeErrMsg = "Please check your date and time configuration under settings"

// ContainersRequest for RPCs
type ContainersRequest struct {
	Ns         string
	ID         string
	Containers []containers.Container
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// GetClient returns a client
func GetClient() *containers.Client {
	if client.CClient == nil {
		var err error
		client.CClient, err = containers.GetClient()
		client.ServerAdress = SystemdClient
		if err != nil {
			log.Error(err.Error)
		}
	}
	return &client
}

// AddContainer adds a container within the specified namespace
func AddContainer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	var container *containers.Container
	vars := mux.Vars(r)
	ns := vars["ns"]
	err := utils.DecodeJSONBody(w, r, &container)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if container.Name == containers.Runtime {
		http.Error(w, "This app can not be updated. Please use upgrade app instead.", http.StatusInternalServerError)
		return
	}

	go client.MakeContainer(ns, container)
	w.WriteHeader(http.StatusOK)
}

// AddContainer adds a container within the specified namespace
func (s *Service) AddContainer(req ContainersRequest, resp *int) error {
	ns := req.Ns
	container := req.Containers[0]
	if container.Name == containers.Runtime {
		return errors.New("This app can not be updated. Please use upgrade app instead")
	}
	_, err := client.MakeContainer(ns, &container)
	if err != nil {
		if strings.Contains(err.Error(), "127.0.0.53:53: server misbehaving") {
			err = errors.New(dateTimeErrMsg)
		}
		containers.HandleMessages(container.Label, logging.Message{Status: "FAILURE", Text: "Failed to install app: " + err.Error()})
	} else {
		containers.HandleMessages(container.Label, logging.Message{Status: "SUCCESS", Text: "Successfully installed app"})
	}
	return err
}

func (s *Service) ResetContainer(req ContainersRequest, resp *int) error {
	ns := req.Ns
	container := req.Containers[0]
	err := client.ResetContainer(ns, container)
	return err
}

func (s *Service) DeleteInactive(req ContainersRequest, resp *int) error {
	ns := req.Ns
	container := req.Containers[0]
	err := client.DeleteInactive(ns, container.Name)
	if err != nil {
		log.Error("Failed to clean app of type ", container.Name, " ", err)
	}
	return err
}

func (s *Service) CleanupImages(ns string, resp *int) error {
	err := client.CleanupImages(ns)
	return err
}

// UpdateAvailable - Checks for available updates for a given container
func (s *Service) UpdateAvailable(req ContainersRequest, resp *bool) error {
	ns := req.Ns
	container := req.Containers[0]
	var err error
	*resp, err = client.UpdateExists(ns, container.Image, container.User, container.Passwd)
	if err != nil {
		if strings.Contains(err.Error(), "127.0.0.53:53: server misbehaving") {
			err = errors.New(dateTimeErrMsg)
		}
		containers.HandleMessages(container.Label, logging.Message{Status: "FAILURE", Text: "Failed to check for updates: " + err.Error()})
	}
	return err
}

func (s *Service) AddContainerFromFile(req ContainersRequest, resp *int) error {
	ns := req.Ns
	container := req.Containers[0]

	_, err := client.MakeContainerFromFile(ns, &container)
	if err != nil {
		containers.HandleMessages(container.Label, logging.Message{Status: "FAILURE", Text: "Failed to install app: " + err.Error()})
	} else {
		containers.HandleMessages(container.Label, logging.Message{Status: "SUCCESS", Text: "Successfully installed app"})
	}
	return err
}

// UpdateContainer updates the specified container
func UpdateContainer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	var container *containers.Container
	vars := mux.Vars(r)
	ns := vars["ns"]

	err := utils.DecodeJSONBody(w, r, &container)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if container.Name == containers.Runtime {
		http.Error(w, "This app can not be updated. Please use upgrade app instead.", http.StatusInternalServerError)
		return
	}

	go client.UpdateContainer(ns, container)
	w.WriteHeader(http.StatusOK)
}

// Upgrade upgrades a list of containers
func Upgrade(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	var containers []*containers.Container
	vars := mux.Vars(r)
	ns := vars["ns"]
	err := utils.DecodeJSONBody(w, r, &containers)
	if err != nil {
		log.Error("Here ", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go client.UpgradeContainers(ns, containers)
}

// Upgrade upgrades a list of containers
func (s *Service) Upgrade(req ContainersRequest, res *int) error {
	cons := req.Containers
	var c []*containers.Container
	for i := 0; i < len(cons); i++ {
		c = append(c, &cons[i])
	}

	err := client.UpgradeContainers(req.Ns, c)
	return err
}

// GetContainer returns a container
func GetContainer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	vars := mux.Vars(r)
	ns := vars["ns"]
	id := vars["container"]

	container, err := client.GetContainer(ns, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	client.GetContainerStatus(&container)

	err = json.NewEncoder(w).Encode(container)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// GetContainer returns a container
func (s *Service) GetContainer(req ContainersRequest, container *containers.Container) error {
	var err error

	*container, err = client.GetContainer(req.Ns, req.ID)
	if err != nil {
		return err
	}
	if container != nil {
		client.GetContainerStatus(container)
	}

	return nil
}

// DeleteContainer ...
func DeleteContainer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	vars := mux.Vars(r)
	ns := vars["ns"]
	id := vars["container"]
	if err := client.DeleteContainer(ns, client.GetContainerIdByName(ns, id), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func ExportContainer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	vars := mux.Vars(r)
	ns := vars["ns"]
	id := vars["container"]
	idd := client.GetContainerIdByName(ns, id)
	container, _ := client.GetContainer(ns, idd)
	file, err := client.CreateImageFromSnapshot(ns, &container)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+id)
	w.Header().Set("Content-Type", "application/octet-stream")
	//w.Header().Set("Content-Length", r.Header.Get("Content-Length"))
	io.Copy(w, bytes.NewReader(file))

}

// DeleteContainer deletes a container
func (s *Service) DeleteContainer(req ContainersRequest, res *int) error {

	err := client.DeleteContainer(req.Ns, client.GetContainerIdByName(req.Ns, req.ID), req.ID)

	return err
}

// GetContainers ...
func GetContainers(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	vars := mux.Vars(r)
	ns := vars["ns"]
	rawContainers, _ := client.GetContainers(ns)
	var containers []containers.Container
	if rawContainers != nil && len(rawContainers) > 0 {
		for _, c := range rawContainers {
			client.GetContainerStatus(&c)
			containers = append(containers, c)
		}
	}
	var err = json.NewEncoder(w).Encode(containers)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// GetContainers returns an array of installed containers
func (s *Service) GetContainers(namespace string, containers *[]containers.Container) error {
	rawContainers, err := client.GetContainers(namespace)
	if err == nil && rawContainers != nil && len(rawContainers) > 0 {
		for _, c := range rawContainers {
			client.GetContainerStatus(&c)
			*containers = append(*containers, c)
		}
	}
	return err
}
func (s *Service) StartContainer(req ContainersRequest, res *int) error {
	c := req.Containers[0]
	err := client.SwitchServiceState(c.Name, true)
	return err
}
func (s *Service) StopContainer(req ContainersRequest, res *int) error {
	c := req.Containers[0]
	err := client.SwitchServiceState(c.Name, false)
	return err
}
func (s *Service) Start(req ContainersRequest, res *int) error {
	_, _, err := client.StartContainer(req.Ns, client.GetContainerIdByName(req.Ns, req.ID))
	if err != nil {
		log.Fatal("Failed to start app:", err.Error())
	}

	return err
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	// Upgrade initial GET request to a websocket
	ws, err := upgrader.Upgrade(w, r, nil)
	//log.Info("WS: ", ws)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	containers.Clients.Mu.Lock()
	defer containers.Clients.Mu.Unlock()
	containers.Clients.Clients[ws] = true

}
