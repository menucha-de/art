package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io/ioutil"
	"net/http"
	"net/rpc"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/containerd/go-cni"
	art "github.com/menucha-de/art/art"
	"github.com/menucha-de/art/art/containers"
	"github.com/menucha-de/logging"
	loglib "github.com/menucha-de/logging"
	"github.com/menucha-de/utils"
	"github.com/sirupsen/logrus"
)

var log *loglib.Logger = logging.GetLogger("art")

func main() {
	var upgrade = flag.String("upgrade", "", "Upgrades app with given app file")

	var user = flag.String("user", "", "User for pulling")
	var pwd = flag.String("pwd", "", "Password for pulling")

	var d = flag.Bool("d", false, "Delete app instance")

	var clean = flag.Bool("clean", false, "Delete inactive apps with given name or all")

	var images = flag.Bool("images", false, "Remove unused app images after clean")

	var all = flag.Bool("all", false, "Delete all inactive apps")

	var start = flag.Bool("s", false, "Starts a apps instance")

	var ns = flag.String("ns", "default", "namespace")
	var containerName = flag.String("n", "", "app name")

	var res = flag.String("res", "", "Check for update for given app")

	var debug = flag.Bool("debug", false, "Set debug level for the application")

	flag.Parse()

	if *debug {
		log.SetLevel(logrus.DebugLevel)
	}

	if len(*res) > 0 {
		updateExists, err := art.GetClient().UpdateExists(*ns, *res, *user, *pwd)
		if err != nil {
			log.Error("Failed to get image version for ", err)
			os.Exit(-1)
		}

		if updateExists {
			log.Info("An update for the image ", *res, " is available")
		} else {
			log.Info("The image ", *res, " is up to date")
		}
		os.Exit(0)
	}

	//Delete
	if *d == true {
		err := art.GetClient().DeleteContainer(*ns, art.GetClient().GetContainerIdByName(*ns, *containerName), *containerName)
		if err != nil {
			log.Error("Failed to delete app ", err)
			os.Exit(-1)
		}
		os.Exit(0)
	}

	if *clean {
		if *all {
			activeCs, err := art.GetClient().GetContainers(*ns)
			if err != nil {
				log.Error("Failed to get containers ", err)
				os.Exit(-1)
			}
			for _, c := range activeCs {
				err := art.GetClient().DeleteInactive(*ns, c.Name)
				if err != nil {
					log.Error("Failed to clean app of type ", c.Name, " ", err)
					os.Exit(-1)
				}
			}
		} else {
			err := art.GetClient().DeleteInactive(*ns, *containerName)
			if err != nil {
				log.Error("Failed to clean app of type ", *containerName, " ", err)
				os.Exit(-1)
			}
		}

		if *images {
			err := art.GetClient().CleanupImages(*ns)
			if err != nil {
				log.Error("Failed to clean up images ", err)
				os.Exit(-1)
			}
		}

		os.Exit(0)
	}

	//Create container instance
	if len(*upgrade) > 0 {
		fileBytes, err := ioutil.ReadFile(*upgrade)
		if err != nil {
			log.Error("Can not read file ", *upgrade)
			os.Exit(-1)
		}
		fileString := string(fileBytes)

		var sContainer []*containers.Container

		if err := json.Unmarshal([]byte(fileString), &sContainer); err != nil {
			fmt.Println("Parse error ", err)
			os.Exit(-1)
		}

		for _, c := range sContainer {
			c.Passwd = *pwd
			if len(*user) > 0 {
				c.User = *user
			}
		}

		err = art.GetClient().UpgradeContainers(*ns, sContainer)
		if err != nil {
			log.Error("Failed to upgrade ", err)
			os.Exit(-1)
		}

		os.Exit(0)
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	//Container lc Start and network creation
	if *start {
		cId := art.GetClient().GetContainerIdByName(*ns, *containerName)
		if len(cId) < 1 {
			log.Fatal("App ", *containerName, " unknown.")
		}
		c, err := art.GetClient().GetContainer(*ns, cId)
		if err != nil {
			log.Fatal("App ", *containerName, " not found in namespace ", *ns)
		}

		var ports []cni.PortMapping
		if !c.IsHostNet {
			ports, err = art.GetClient().GetPortMapping(*ns, cId)
			if err != nil {
				log.WithError(err).Fatal("Failed to get portmapping for ", *containerName)
			}
			err = containers.CreateNetwork("/var/run/netns/"+c.Name, cId, c.Name, ports)
			if err != nil {
				log.WithError(err).Fatal("Failed to create network for ", *containerName)
			}
		}

		task, tCh, err := art.GetClient().StartContainer(*ns, cId)
		if err != nil {
			log.WithError(err).Fatal("Failed to start app ", *containerName)
		}

		select {
		case <-tCh:
			log.Debug("App stopped failed execution ", *containerName)
		case <-done:
			log.Debug("App stopped with sig term ", *containerName)
		}

		if !c.IsHostNet {
			err = containers.TearDownNetwork("/var/run/netns/"+c.Name, cId, c.Name, ports)
			if err != nil {
				log.Error("Failed to tear down network ", err)
			}
		}

		art.GetClient().StopContainer(*ns, cId, task)
		os.Exit(0)
	}

	log = loglib.GetLogger("art")

	art.AddRoutes(loglib.LogRoutes)

	router := art.NewRouter()

	router.NotFoundHandler = http.HandlerFunc(notFound)

	var s = new(art.Service)

	rpcServer := rpc.NewServer()
	rpcServer.Register(s)

	router.Handle(rpc.DefaultRPCPath, rpcServer)

	srv := &http.Server{
		Addr:    "art:8080",
		Handler: router,
	}

	errs := make(chan error)

	c := art.GetClient()

	if c.CClient == nil {
		log.Fatal("Failed to init app client")
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()

	log.Debug("Server Started on port 8080")

	select {
	case err := <-errs:
		log.WithError(err).Error("Could not start art service")
	case <-done:
		log.Debug("Server Stopped")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	defer func() {
		cancel()
	}()

	if err := srv.Shutdown(ctx); err != nil {
		log.WithError(err).Fatal("Server art Shutdown Failed")
	}

	log.Debug("Server art Exited Properly")

}

func notFound(w http.ResponseWriter, r *http.Request) {
	if !(r.Method == "GET") {
		w.WriteHeader(404)
	}
	file := "./www" + html.EscapeString(r.URL.Path)
	if file == "./www/" {
		file = "./www/index.html"
	}
	if utils.FileExists(file) {
		http.ServeFile(w, r, file)
	} else {
		w.WriteHeader(404)
	}
}
