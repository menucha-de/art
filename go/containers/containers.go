package containers

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/cmd/ctr/commands"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/images/archive"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/oci"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/remotes/docker/config"
	"github.com/containerd/go-cni"
	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/peramic/App.Systemd/service"
	"github.com/peramic/logging"
	"github.com/txn2/txeh"
)

var (
	log *logging.Logger = logging.GetLogger("art")
)

const (
	Default           = "default"
	System            = "system"
	Runtime           = "runtime"
	ContainerConf     = "config"
	ExposedPorts      = "ExposedPorts"
	ExposedPortsLabel = "org.opencontainers.image.exposedPorts"

	ContainerDSock = "/run/containerd/containerd.sock"
	LockFile       = "/run/lock/art"
	MountBaseDir   = "/var/lib/apps/"

	Name        = "NAME"
	Label       = "LABEL"
	Active      = "IS_ACTIVE"
	HostNetwork = "NETHOST"

	Hostname = "HOSTNAME"
)

//SystemdCalls
const (
	Create      = "Service.CreateService"
	SwitchState = "Service.SwitchServiceState"
	GetStatus   = "Service.GetStatus"
	Upgrade     = "Service.Upgrade"
	Delete      = "Service.DeleteUnitFile"
	HealthCheck = "Service.HealthCheck"
)

// Init ...
// func init() {
// 	go func() {
// 		ticker := time.NewTicker(5 * time.Second)
// 		for {
// 			select {
// 			case <-ticker.C:
// 				Clients.Mu.Lock()
// 				for client := range Clients.Clients {
// 					client.SetWriteDeadline(time.Now().Add(3 * time.Second))
// 					if err := client.WriteMessage(websocket.PingMessage, nil); err != nil {
// 						log.Debug("Connection closed from keepalive")
// 						client.Close()
// 						delete(Clients.Clients, client)
// 					}
// 				}
// 				Clients.Mu.Unlock()
// 			}
// 		}
// 	}()
// }

func (c *Client) Close() {
	c.CClient.Close()
}

func GetClient() (*containerd.Client, error) {
	client, err := containerd.New(ContainerDSock)
	if err != nil {
		log.Error(debug.Stack())
		return nil, err
	}
	return client, nil
}

func TearDownNetwork(netns string, id string, container string, ports []cni.PortMapping) error {
	ctx := context.Background()

	ifPrefixName := "eth"
	// Initializes library
	l, err := cni.New(
		// one for loopback network interface
		cni.WithMinNetworkCount(2),
		cni.WithPluginConfDir("/etc/cni/net.d"),
		cni.WithPluginDir([]string{"/usr/lib/cni"}),
		// Sets the prefix for network interfaces, eth by default
		cni.WithInterfacePrefix(ifPrefixName))
	if err != nil {
		log.Error("failed to initialize cni library: %v", err)
		return err
	}

	// Load the cni configuration
	if err := l.Load(cni.WithLoNetwork, cni.WithDefaultConf); err != nil {
		log.Error("failed to load cni configuration: %v", err)
		return err
	}

	log.Debug("Teardown network ", id, " ", netns)
	if err := l.Remove(ctx, id, netns, cni.WithCapabilityPortMap(ports)); err != nil {
		//Ignoring failed to remove
		log.Debug("failed to teardown network: %v", err)
	}

	//Finally delete netns
	cmd := exec.Command("ip", "netns", "del", container)
	stdout, err := cmd.Output()
	log.Debug("rm netns ", stdout, " ", err)

	return nil
}

func CreateNetwork(netns string, id string, container string, ports []cni.PortMapping) error {
	//Cleanup
	fd, err := syscall.Open(LockFile, syscall.O_CREAT|syscall.O_RDONLY, 0600)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	err = syscall.Flock(fd, syscall.LOCK_EX)
	if err != nil {
		return err
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)

	cmd := exec.Command("ip", "netns", "del", container)
	stdout, err := cmd.Output()
	log.Debug("rm netns ", stdout, " ", err)

	cmd = exec.Command("ip", "netns", "add", container)
	stdout, err = cmd.Output()
	log.Debug("add netns ", stdout, " ", err)

	ifPrefixName := "eth"
	defaultIfName := "eth0"

	// Initializes library
	l, err := cni.New(
		// one for loopback network interface
		cni.WithMinNetworkCount(2),
		cni.WithPluginConfDir("/etc/cni/net.d"),
		cni.WithPluginDir([]string{"/usr/lib/cni"}),
		// Sets the prefix for network interfaces, eth by default
		cni.WithInterfacePrefix(ifPrefixName))
	if err != nil {
		log.Error("failed to initialize cni library: %v", err)
		return err
	}
	// Load the cni configuration
	if err := l.Load(cni.WithLoNetwork, cni.WithDefaultConf); err != nil {
		log.Error("failed to load cni configuration: %v", err)
		return err
	}

	//TODO Load and deserialize config, rename network

	ctx := context.Background()

	log.Debug("Teardown network before reinit ", id, " ", netns)
	if err := l.Remove(ctx, id, netns, cni.WithCapabilityPortMap(ports)); err != nil {
		//Ignoring failed to remove
		log.Error("failed to teardown network: %v", err)
	}

	result, err := l.Setup(ctx, id, netns, cni.WithCapabilityPortMap(ports))

	if err != nil {
		log.Error("failed to setup network for namespace %s: %s for app ", netns, id, err)
		return err
	}

	// Get IP of the default interface
	IP := result.Interfaces[defaultIfName].IPConfigs[0].IP.String()
	log.Debug("IP of the default interface %s:%s\n", defaultIfName, IP)

	hosts, err := txeh.NewHostsDefault()
	if err != nil {
		log.Error("Failed to add hosts ", err)
		return err
	}
	hosts.AddHost(IP, container)
	hosts.Save()

	return err
}

func (c *Client) UpdateExists(ns string, ref string, user string, passwd string) (bool, error) {
	resolve := getResolver(ns, user, passwd)
	ctx := namespaces.WithNamespace(context.Background(), ns)
	_, desc, err := resolve.Resolve(ctx, ref)
	if err == nil {
		img, err := c.CClient.GetImage(ctx, ref)
		if err == nil && img != nil {
			manifest := img.Target()
			return desc.Digest != manifest.Digest, nil

		}
	} else {
		return false, err
	}
	return true, nil
}

func getResolver(ns string, user string, passwd string) remotes.Resolver {
	options := docker.ResolverOptions{
		Tracker: commands.PushTracker,
	}
	hostOptions := config.HostOptions{}

	if len(user) > 0 {
		hostOptions.Credentials = func(host string) (string, string, error) {
			// If host doesn't match...
			// Only one host
			return user, passwd, nil
		}
	}

	tls := &tls.Config{}
	tls.InsecureSkipVerify = true
	hostOptions.DefaultTLS = tls

	ctx := namespaces.WithNamespace(context.Background(), ns)

	options.Hosts = config.ConfigureHosts(ctx, hostOptions)

	return docker.NewResolver(options)

}

func (c *Client) pull(ns string, img string, user string, passwd string, label string) (containerd.Image, error) {
	ctx := namespaces.WithNamespace(context.Background(), ns)

	ongoing := newJobs(img)
	pctx, stopProgress := context.WithCancel(ctx)
	progress := make(chan struct{})

	go func() {
		showProgress(pctx, ongoing, c.CClient.ContentStore(), label)

		close(progress)
	}()
	h := images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		if desc.MediaType != images.MediaTypeDockerSchema1Manifest {
			ongoing.add(desc)
		}
		return nil, nil
	})

	image, err := c.CClient.Pull(ctx, img, containerd.WithPullUnpack,
		containerd.WithResolver(getResolver(ns, user, passwd)), containerd.WithImageHandler(h))
	//containerd.WithPullSnapshotter("native"))
	stopProgress()
	if err != nil {
		return nil, err
	}
	<-progress
	log.Debug("Successfully pulled %s image\n", image.Name())
	return image, nil
}

func (c *Client) detachActive(ns string, name string) error {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	containers, err := c.CClient.Containers(ctx)
	if err != nil {
		log.Error(err)
		return err
	}

	for _, container := range containers {

		labels, _ := container.Labels(ctx)
		if labels[Name] == name {
			log.Debug("Set %s:%s IS_ACTIVE to false", name, container.ID())
			labels[Active] = "false"
			_, err := container.SetLabels(ctx, labels)
			if err != nil {
				log.Error("Can not set Labels ", err)
			}
		}

	}
	return nil
}

func (c *Client) create(ns string, image containerd.Image, container *Container) (containerd.Container, error) {
	var opts []oci.SpecOpts
	opts = append(opts, oci.WithImageConfig(image))

	if container.Devices != nil {
		for _, d := range container.Devices {
			_, err := os.Stat(d.Path)
			if !os.IsNotExist(err) {
				realPath, err := filepath.EvalSymlinks(d.Path)
				log.Debug("Real Path: ", realPath, " : ", err)
				if err == nil {
					_, err := os.Stat(realPath)
					if !os.IsNotExist(err) {
						opts = append(opts, oci.WithLinuxDevice(realPath, "rwm"))
					}
				}
			}
		}
	}

	host := false
	if container.Namespaces != nil {
		for _, lns := range container.Namespaces {
			if lns.Type_ == "network" {
				host = true
				break
			}
		}
	}
	if !host {
		opts = append(opts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.LinuxNamespaceType("network"),
			Path: "/var/run/netns/" + container.Name,
		}))
		opts = append(opts, oci.WithHostHostsFile, oci.WithHostResolvconf)
	} else {
		opts = append(opts, oci.WithHostNamespace(specs.NetworkNamespace), oci.WithHostHostsFile, oci.WithHostResolvconf)
	}

	containerData := MountBaseDir + container.Name + "/"

	if container.Mounts != nil {
		mounts := make([]specs.Mount, 0)
		for _, m := range container.Mounts {
			mount := specs.Mount{}
			mount.Type = "bind"
			//TODO Escape Source special chars (e.g. ../)
			mount.Source = containerData + m.Source
			mount.Destination = m.Destination
			mount.Options = []string{"rbind", "rw"}
			mounts = append(mounts, mount)
		}
		opts = append(opts, oci.WithMounts(mounts))
	}

	opts = append(opts, oci.WithEnv([]string{"LOGHOST=" + container.Name}))

	if container.Capabilities != nil {
		opts = append(opts, oci.WithAddedCapabilities(container.Capabilities))
	}

	ctx := namespaces.WithNamespace(context.Background(), ns)

	// create a container
	containerD, err := c.CClient.NewContainer(
		ctx,
		container.Id,
		containerd.WithImage(image),
		//containerd.WithSnapshotter("native"),
		containerd.WithNewSnapshot(container.Id, image),
		containerd.WithNewSpec(
			opts...,
		),
	)

	if err != nil {
		return nil, err
	}

	labels := map[string]string{Name: container.Name, Active: "true", Label: container.Label, HostNetwork: strconv.FormatBool(host)}

	conf, _ := image.Config(ctx)
	ra, _ := image.ContentStore().ReaderAt(ctx, ocispec.Descriptor{Digest: conf.Digest})
	spec, _ := ioutil.ReadAll(content.NewReader(ra))

	var specinfo map[string]json.RawMessage
	json.Unmarshal(spec, &specinfo)
	json.Unmarshal(specinfo[ContainerConf], &specinfo)
	labels[ExposedPortsLabel] = string(specinfo[ExposedPorts])

	c.Mutex.Lock()
	defer c.Mutex.Unlock()
	//Detach old instances
	c.detachActive(ns, container.Name)
	containerD.SetLabels(ctx, labels)

	//Copy existing data from mounts
	err = c.copyExistingData(ns, *container)

	return containerD, nil
}

func (c *Client) copyExistingData(ns string, container Container) error {
	folderInTarget := make(map[string]string)
	containerData := MountBaseDir + container.Name + "/"
	if container.Mounts != nil {
		for _, m := range container.Mounts {
			source := containerData + m.Source
			_, err := os.Stat(source)
			//Init mount points with exisiting data
			if os.IsNotExist(err) {
				log.Debug("Creating dir ", source)
				os.MkdirAll(source, 0644)
				folderInTarget[m.Destination] = source
			}
		}
	}
	if len(folderInTarget) > 0 {
		log.Debug("Create tmp dir")
		root := "/tmp/" + container.Name

		os.MkdirAll(root, 0644)
		mounts, _ := c.GetMounts(ns, container.Id)
		if len(mounts) > 0 {
			log.Debug("Mounts: ", mounts[0].Source, "   ####    ", strings.Join(mounts[0].Options, ","))
			err := syscall.Mount(mounts[0].Source, root, mounts[0].Type, 0, strings.Join(mounts[0].Options, ","))
			if err != nil {
				log.Debug("Failed to temporary mount", err)
				return err
			}
		}
		for destination, src := range folderInTarget {
			//Mount container
			cmd := exec.Command("cp", "-aT", root+"/"+destination, src)
			log.Debug("Copy ", root+"/"+destination, "to ", src)
			log.Debug("Copying ", cmd.Run())
		}
		syscall.Unmount(root, 0)
		os.RemoveAll(root)
	}
	return nil
}

func (c *Client) createService(ns string, id string, name string, desciption string) error {
	request := service.CreateRequest{
		Ns:          ns,
		ID:          id,
		Name:        name,
		Description: desciption,
	}
	var response int

	err := c.Call(Create, request, &response)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) GetMounts(ns string, id string) ([]mount.Mount, error) {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	snapshotter := c.CClient.SnapshotService("")
	log.Debug("Get Mounts for ", id)
	mounts, err := snapshotter.Mounts(ctx, id)
	if err != nil {
		return nil, err
	}
	return mounts, nil
}

func (c *Client) SwitchServiceState(name string, state bool) error {
	request := service.StateRequest{
		Name:    name,
		Enabled: state,
		Active:  state,
	}
	var response int
	err := c.Call(SwitchState, request, &response)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) GetContainers(ns string) ([]Container, error) {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	containers, err := c.CClient.Containers(ctx)
	var result []Container
	if err != nil {
		log.Error(err)
		return nil, err
	}

	for _, cont := range containers {
		str, err := c.fillContainerData(ns, cont)
		if err != nil {
			return nil, err
		}
		if str.IsActive {
			result = append(result, str)
		}
	}
	return result, nil
}

func (c *Client) CleanupImages(ns string) error {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	imageStore := c.CClient.ImageService()
	imageList, err := imageStore.List(ctx)
	if err != nil {
		return err
	}
	activeCs, err := c.GetContainers(ns)
	if err != nil {
		return err
	}

	for _, image := range imageList {
		found := false
		for _, c := range activeCs {
			if image.Name == c.Image {
				found = true
			}
		}
		if !found {
			err := imageStore.Delete(ctx, image.Name)
			if err != nil {
				return err
			}
			log.Info("Image ", image.Name, " deleted")
		}
	}
	return nil
}

func (c *Client) DeleteInactive(ns string, name string) error {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	containers, err := c.CClient.Containers(ctx)
	if err != nil {
		log.Error(err)
		return err
	}

	for _, container := range containers {
		str, err := c.fillContainerData(ns, container)
		if err != nil {
			return err
		}
		if !str.IsActive {
			if str.Name == name {
				log.Info("Container ", str.Id, " of type ", name, " deleted")
				err = c.DeleteContainer(ns, str.Id, name, true)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *Client) GetContainerIdByName(ns string, name string) string {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	containers, err := c.CClient.Containers(ctx)
	if err != nil {
		return ""
	}
	for _, cont := range containers {
		labels, _ := cont.Labels(ctx)
		cName := ""
		isActive := false
		if val, ok := labels["NAME"]; ok {
			cName = val
		}
		if val, ok := labels["IS_ACTIVE"]; ok {
			isActive, _ = strconv.ParseBool(val)
		}
		if name == cName && isActive {
			return cont.ID()
		}
	}

	// containers, _ := c.GetContainers(ns)
	// for _, c := range containers {
	// 	if c.Name == name {
	// 		return c.Id
	// 	}
	// }
	return ""
}

func (c *Client) GetPortMapping(ns string, id string) ([]cni.PortMapping, error) {
	container := c.getContainer(ns, id)
	ctx := namespaces.WithNamespace(context.Background(), ns)
	if container != nil {
		var ports = []cni.PortMapping{}
		labels, err := container.Labels(ctx)
		if err != nil {
			return nil, err
		}
		strPorts := labels[ExposedPortsLabel]
		if len(strPorts) > 0 {
			var rawPorts map[string]json.RawMessage
			err = json.Unmarshal([]byte(strPorts), &rawPorts)
			if err != nil {
				return ports, nil
			}
			for key := range rawPorts {
				rawKey := strings.Split(key, "/")
				if len(rawKey) > 1 {
					port, err := strconv.Atoi(rawKey[0])
					if err != nil {
						continue
					}
					protocol := rawKey[1]
					if (protocol == "tcp" || protocol == "udp") && port != 80 {
						ports = append(ports, cni.PortMapping{
							HostPort:      int32(port),
							ContainerPort: int32(port),
							Protocol:      protocol,
						})
					}
				}
			}
		}
		return ports, nil
	}

	return nil, errors.New("App " + id + " does not exists")
}

func (c *Client) getContainer(ns string, id string) containerd.Container {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	containers, _ := c.CClient.Containers(ctx)
	for _, container := range containers {
		if container.ID() == id {
			return container
		}
	}
	return nil
}

func (c *Client) GetContainer(ns string, id string) (Container, error) {
	var result Container
	var err error
	container := c.getContainer(ns, id)
	if container != nil {
		result, err = c.fillContainerData(ns, container)
		if err != nil {
			return Container{}, err
		}
	}
	return result, nil
}

func (c *Client) GetContainerStatus(result *Container) error {
	request := service.UnitRequest{
		Name: result.Name,
	}
	var response dbus.UnitStatus
	err := c.Call(GetStatus, request, &response)
	if err != nil {
		return err
	}
	log.Debug("State: response.ActiveState", " ", response.SubState)

	if response.ActiveState == "active" {
		if response.SubState == "running" {
			result.State = Started
		} else {
			result.State = Starting
		}

	} else {
		result.State = Stopped
	}
	return nil
}

func (c *Client) fillContainerData(ns string, container containerd.Container) (Container, error) {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	var result Container
	if container != nil {
		result.Id = container.ID()
		labels, _ := container.Labels(ctx)

		if val, ok := labels[Name]; ok {
			result.Name = val
		} else {
			result.Name = container.ID()
		}
		if val, ok := labels[Label]; ok {
			result.Label = val
		} else {
			result.Label = container.ID()
		}
		if val, ok := labels[Active]; ok {
			result.IsActive, _ = strconv.ParseBool(val)
		}
		if val, ok := labels[HostNetwork]; ok {
			result.IsHostNet, _ = strconv.ParseBool(val)
		}

		img, err := container.Image(ctx)
		if err != nil {
			return Container{}, err
		}
		result.Image = img.Name()
		fields := strings.SplitAfter(img.Name(), ":")
		if len(fields) > 0 {
			result.Version = fields[len(fields)-1]
		}
	}
	return result, nil
}

func (c *Client) UpgradeContainers(ns string, containers []*Container) error {
	upgradeBase := false
	for _, container := range containers {
		//shouldn't we delete first?
		_, err := c.MakeContainer(ns, container)
		if err != nil {
			HandleMessages(container.Label, logging.Message{Status: "FAILURE", Text: "Failed to update app: " + err.Error()})
			return err
		}
		if container.Name == Runtime {
			upgradeBase = true
		} else {
			HandleMessages(container.Label, logging.Message{Status: "SUCCESS", Text: "Successfully updated app"})
		}
	}

	if upgradeBase {
		runtimeMount, _ := c.GetMounts(ns, c.GetContainerIdByName(ns, Runtime))

		request := service.UpgradeRequest{}

		request.Hostname = os.Getenv(Hostname)
		log.Debug(Hostname+" ", request.Hostname)
		for _, mount := range runtimeMount {
			request.MountOptions = strings.ReplaceAll(strings.Join(mount.Options, ","), "/var/lib/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/", "")
			log.Debug("Runtime mounts ", request.MountOptions)
		}
		var response int
		err := c.Call(Upgrade, request, &response)
		if err != nil {
			HandleMessages("Runtime", logging.Message{Status: "FAILURE", Text: "Failed to upgrade runtime: " + err.Error()})
			log.Error(err.Error())
		}
		HandleMessages("Runtime", logging.Message{Status: "SUCCESS", Text: "Successfully upgraded runtime"})
	}
	return nil
}

func (c *Client) UpdateContainer(ns string, container *Container) error {
	err := c.DeleteContainer(ns, container.Id, container.Name)
	if err != nil {
		return err
	}
	_, err = c.MakeContainer(ns, container)

	return err
}

// DeleteContainer ...
func (c *Client) DeleteContainer(ns string, id string, name string, ignoreService ...bool) error {
	HandleMessages(name, logging.Message{Status: "PENDING", Text: "Deleting ... "})
	ctx := namespaces.WithNamespace(context.Background(), ns)
	container := c.getContainer(ns, id)
	if container != nil {
		if len(ignoreService) == 0 || (len(ignoreService) > 0 && !ignoreService[0]) {
			request := service.DeleteRequest{
				Name: name,
			}
			var response int
			err := c.Call(Delete, request, &response)
			if err != nil {
				HandleMessages(name, logging.Message{Status: "FAILURE", Text: "Failed to delete app: " + err.Error()})
				log.WithError(err).Error("Failed to delete app")
				//don't return error we should delete container anyway
			} else {
				HandleMessages(name, logging.Message{Status: "SUCCESS", Text: "Successfully deleted app "})
			}
		}

		/*img := ""
		x, err := container.Image(ctx)
		if err == nil {
			img = x.Name()
		}*/
		if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
			return err
		}
		/*imageStore := c.CClient.ImageService()
		if err := imageStore.Delete(ctx, img); err != nil {
			if !errdefs.IsNotFound(err) {

				log.WithError(err).Errorf("unable to delete %v", name)

			}
			// image ref not found in metadata store; log not found condition
			log.Error("image not found ", name)
		}*/
	} else {
		return errors.New("Failed to delete app. App " + name + " not exists")
	}
	return nil
}

func (c *Client) MakeContainerFromFile(ns string, container *Container) (*Container, error) {
	id := uuid.New()
	container.Id = id.String()
	file := container.Name
	ctx := namespaces.WithNamespace(context.Background(), ns)
	reader, err := os.Open("/opt/" + file)
	if err != nil {
		return nil, err
	}
	defer os.Remove("/opt/" + file)

	opts := []containerd.ImportOpt{
		containerd.WithImageRefTranslator(archive.AddRefPrefix(file)),
	}
	imgs, err := c.CClient.Import(ctx, reader, opts...)
	closeErr := reader.Close()
	if err != nil {
		log.Error(err)
		return nil, err
	}
	if closeErr != nil {
		log.Error(closeErr)
		return nil, closeErr
	}

	//var metadata images.Image
	/*for _, imgrec := range imgs {
		fmt.Println(imgrec.Name)
		if imgrec.Name. == file {
			metadata = imgrec
			fmt.Println("--------------------------")
			continue
		}
		err = c.CClient.ImageService().Delete(ctx, imgrec.Name)
		if err != nil {
			return nil, err
		}
	}*/
	img := containerd.NewImage(c.CClient, imgs[0])
	err = img.Unpack(ctx, "native")
	if err != nil {
		log.Error(err)
		return nil, err
	}

	_, err = c.create(ns, img, container)
	if err != nil {
		log.Error("Failed to create app ", container.Name, " ", err)
		return nil, err
	}
	err = c.createService(ns, container.Name, container.Name, container.Label+" Service")
	if err != nil {
		log.Error("Failed to create service ", err)
		return nil, err
	}

	if len(container.State) > 0 && (container.State == Starting || container.State == Started) {
		err = c.SwitchServiceState(container.Name, true)
	} else {
		err = c.SwitchServiceState(container.Name, false)
	}
	if err != nil {
		log.Error("Failed to switch service state ", err)
		return nil, err
	}

	return container, nil

}

func (c *Client) CreateContainer(ns string, img containerd.Image, container *Container) (*Container, error) {
	_, err := c.create(ns, img, container)
	if err != nil {
		log.Error("Failed to create app ", container.Name, " ", err)
		return nil, err
	}

	if container.Name != Runtime {
		err = c.createService(ns, container.Name, container.Name, container.Label+" Service")
		if err != nil {
			log.Error("Failed to create service ", err)
			return nil, err
		}

		if len(container.State) > 0 && (container.State == Starting || container.State == Started) {
			err = c.SwitchServiceState(container.Name, true)
		} else {
			err = c.SwitchServiceState(container.Name, false)
		}
		if err != nil {
			log.Error("Failed to switch service state ", err)
			return nil, err
		}
	}
	return container, nil
}

func (c *Client) MakeContainer(ns string, container *Container) (*Container, error) {
	var img containerd.Image
	var err error

	id := uuid.New()
	container.Id = id.String()
	img, err = c.pull(ns, container.Image, container.User, container.Passwd, container.Label)
	if err != nil {
		log.Error("Failed to pull app ", container.Name, " ", err)
		return nil, err
	}

	return c.CreateContainer(ns, img, container)
}

func (c *Client) ResetContainer(ns string, container Container) error {
	var err error
	if len(container.Name) > 0 {
		c.SwitchServiceState(container.Name, false)
		os.RemoveAll(MountBaseDir + "/" + container.Name)

		//Delete container
		ctx := namespaces.WithNamespace(context.Background(), ns)
		cd := c.getContainer(ns, container.Id)
		if cd == nil {
			return errors.New("Failed to reset container " + container.Name + " during re-init. Container not found.")
		}

		img, err := cd.Image(ctx)
		if err != nil {
			return err
		}
		err = cd.Delete(ctx)
		if err != nil {
			return err
		}

		//Re-Init new instance
		container.Id = uuid.New().String()
		if img == nil {
			return errors.New("Failed to reset container " + container.Name + " during re-init. Image not found.")
		}
		_, err = c.CreateContainer(ns, img, &container)
		if err != nil {
			return err
		}
		err = c.copyExistingData(ns, container)

		if container.State == Starting || container.State == Started {
			c.SwitchServiceState(container.Name, true)
		} else {
			c.SwitchServiceState(container.Name, false)
		}

	}
	return err
}

func (c *Client) StopContainer(ns string, container string, task containerd.Task) error {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	exitStatusC, err := task.Wait(ctx)
	if err != nil {
		log.Error("Failed to get exit status ", err)
	}

	if err := task.Kill(ctx, syscall.SIGTERM); err != nil {
		log.Debug("Failed to terminate task ", err)
	}

	ticker := time.NewTicker(3 * time.Second)

	force := false

	select {
	case <-ticker.C:
		log.Debug("Forced to kill task")
		if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
			log.Error("Failed to kill task ", err)
		}
		force = true
	case exit := <-exitStatusC:
		log.Debug("Terminate task with exit code ", exit)
	}

	if force {
		log.Debug("Exit forcely: ", <-exitStatusC)
	}

	if _, err := task.Delete(ctx); err != nil {
		return err
	}

	fd, err := syscall.Open(LockFile, syscall.O_CREAT|syscall.O_RDONLY, 0600)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	err = syscall.Flock(fd, syscall.LOCK_EX)
	if err != nil {
		return err
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)

	hosts, err := txeh.NewHostsDefault()
	if err != nil {
		return err
	}

	str, err := c.fillContainerData(ns, c.getContainer(ns, container))
	hosts.RemoveHost(str.Name)
	hosts.Save()

	return nil
}

// func (c Client) cleanupTask(task containerd.Task) error {
// 	if _, err := task.Delete(ctx); err != nil {
// 		return err
// 	}
// }

func (c *Client) StartContainer(ns string, id string) (containerd.Task, <-chan containerd.ExitStatus, error) {
	var task containerd.Task
	var exitStatusC <-chan containerd.ExitStatus
	container := c.getContainer(ns, id)
	if container != nil {
		ctx := namespaces.WithNamespace(context.Background(), ns)
		var err error
		task, err = container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
		if err != nil {
			return nil, nil, err
		}
		exitStatusC, err = task.Wait(ctx)
		if err := task.Start(ctx); err != nil {
			return nil, nil, err
		}
	} else {
		return nil, nil, errors.New("Container " + id + " not found in namespace " + ns)
	}

	return task, exitStatusC, nil
}
func (c *Client) CreateImageFromSnapshot(ns string, container *Container) ([]byte, error) {
	ctx := namespaces.WithNamespace(context.Background(), ns)
	ctx, done, err := c.CClient.WithLease(ctx)
	if err != nil {
		log.Error(err)
		return nil, err
	}
	defer done(ctx)
	// First, let's get the parent image manifest so that we can
	// later create a new one from it, with a new layer added to it.
	cont := c.getContainer(ns, container.Id)
	img, err := cont.Image(ctx)
	name := img.Name()
	if err != nil {
		log.Error(err)
		return nil, err
	}
	m, err := LoadManifest(ctx, c.CClient.ContentStore(), img.Target())

	if err != nil {
		log.Error(err)
		return nil, err
	}

	snapshotter := c.CClient.SnapshotService("")
	snapshot, err := snapshotter.Stat(ctx, container.Id)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	upperMounts, err := snapshotter.Mounts(ctx, container.Id)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	lowerMounts, err := snapshotter.View(ctx, "temp-readonly-parent", snapshot.Parent)
	if err != nil {
		log.Error(err)
		return nil, err
	}
	defer snapshotter.Remove(ctx, "temp-readonly-parent")

	// Generate a diff in content store
	diffs, err := c.CClient.DiffService().Compare(ctx,
		lowerMounts,
		upperMounts,
		diff.WithMediaType(ocispec.MediaTypeImageLayerGzip),
		diff.WithReference("custom-ref"))
	if err != nil {
		log.Error(err)
		return nil, err
	}

	// Add our new layer to the image manifest
	err = m.AddLayer(ctx, c.CClient.ContentStore(), diffs)

	// Let's see if the image exists already, if so, let's delete it
	_, err = c.CClient.GetImage(ctx, name)
	if err == nil {

		c.CClient.ImageService().Delete(ctx, name, images.SynchronousDelete())
	}
	wb := bytes.NewBuffer(nil)
	_, err = c.CClient.ImageService().Create(ctx,
		images.Image{
			Name: name,
			Target: ocispec.Descriptor{
				Digest:    m.Descriptor().Digest,
				Size:      m.Descriptor().Size,
				MediaType: m.Descriptor().MediaType,
			},
		})
	if err != nil {
		log.Error(err)
		return nil, err
	}

	// This will create the required snapshot for the new layer,
	// which will allow us to run the image immediately.
	xx, err := c.CClient.GetImage(ctx, name)

	err = c.CClient.Export(ctx, wb, archive.WithPlatform(platforms.Default()), archive.WithImage(c.CClient.ImageService(), xx.Name()))
	//err = imageBuilt.Unpack(ctx, containerd.DefaultSnapshotter)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	/*tarFile, err := os.Create("ccccc")
	if err != nil {
		log.Error(err)
	}
	defer tarFile.Close()
	tarFile.Write(wb.Bytes())*/

	return wb.Bytes(), nil
}
