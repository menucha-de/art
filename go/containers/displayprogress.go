package containers

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/pkg/progress"
	"github.com/containerd/containerd/remotes"
	"github.com/gorilla/websocket"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/peramic/logging"
)

type wbSocketClients struct {
	Clients map[*websocket.Conn]bool
	Mu      sync.RWMutex
}

//Clients websocket opened connections
var Clients = wbSocketClients{Clients: make(map[*websocket.Conn]bool)}

func HandleMessages(topic string, msg logging.Message) {
	Clients.Mu.Lock()
	defer Clients.Mu.Unlock()
	for client := range Clients.Clients {
		client.SetWriteDeadline(time.Time{})
		err := client.WriteJSON(msg)
		if err != nil {
			log.Debug("WebSocket client connection lost while sending message ", err.Error())
			client.Close()
			delete(Clients.Clients, client)
		}
	}
	//log.Info(msg)
	//log.Notify(msg.App, msg.Status+": "+msg.Mess)
	//log.Info(msg.App, logrus.InfoLevel, msg.Status+": "+msg.Mess)
	logging.Notify(topic, msg)
}

type jobs struct {
	name     string
	added    map[digest.Digest]struct{}
	descs    []ocispec.Descriptor
	mu       sync.Mutex
	resolved bool
}

func newJobs(name string) *jobs {
	return &jobs{
		name:  name,
		added: map[digest.Digest]struct{}{},
	}
}

func (j *jobs) add(desc ocispec.Descriptor) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.resolved = true

	if _, ok := j.added[desc.Digest]; ok {
		return
	}
	j.descs = append(j.descs, desc)
	j.added[desc.Digest] = struct{}{}
}

func (j *jobs) jobs() []ocispec.Descriptor {
	j.mu.Lock()
	defer j.mu.Unlock()

	var descs []ocispec.Descriptor
	return append(descs, j.descs...)
}

func (j *jobs) isResolved() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.resolved
}

// StatusInfo holds the status info for an upload or download
type StatusInfo struct {
	Ref       string
	Status    string
	Offset    int64
	Total     int64
	StartedAt time.Time
	UpdatedAt time.Time
}

func showProgress(ctx context.Context, ongoing *jobs, cs content.Store, name string) {
	var (
		ticker   = time.NewTicker(100 * time.Millisecond)
		start    = time.Now()
		statuses = map[string]StatusInfo{}
		done     bool
		files    = map[string]struct{}{}
		//totalfiles int
	)
	defer ticker.Stop()
outer:
	for {
		select {
		case <-ticker.C:
			resolved := "resolved"
			if !ongoing.isResolved() {
				resolved = "resolving"
			}
			statuses[ongoing.name] = StatusInfo{
				Ref:    ongoing.name,
				Status: resolved,
			}
			keys := []string{ongoing.name}

			activeSeen := map[string]struct{}{}
			if !done {
				active, err := cs.ListStatuses(ctx, "")
				if err != nil {
					log.WithError(err).Error("active check failed")
					continue
				}
				for _, active := range active {

					statuses[active.Ref] = StatusInfo{
						Ref:       active.Ref,
						Status:    "downloading",
						Offset:    active.Offset,
						Total:     active.Total,
						StartedAt: active.StartedAt,
						UpdatedAt: active.UpdatedAt,
					}

					activeSeen[active.Ref] = struct{}{}
				}
			}

			for _, j := range ongoing.jobs() {
				key := remotes.MakeRefKey(ctx, j)
				keys = append(keys, key)
				if _, ok := activeSeen[key]; ok {
					continue
				}

				status, ok := statuses[key]
				if !done && (!ok || status.Status == "downloading") {
					info, err := cs.Info(ctx, j.Digest)
					if err != nil {
						if !errdefs.IsNotFound(err) {
							log.WithError(err).Errorf("failed to get content info")
							continue outer
						} else {
							statuses[key] = StatusInfo{
								Ref:    key,
								Status: "waiting",
							}
						}
					} else if info.CreatedAt.After(start) {
						statuses[key] = StatusInfo{
							Ref:       key,
							Status:    "done",
							Offset:    info.Size,
							Total:     info.Size,
							UpdatedAt: info.CreatedAt,
						}
					} else {
						statuses[key] = StatusInfo{
							Ref:    key,
							Status: "exists",
						}
					}
				} else if done {
					if ok {
						if status.Status != "done" && status.Status != "exists" {
							status.Status = "done"
							statuses[key] = status
						}
					} else {
						statuses[key] = StatusInfo{
							Ref:    key,
							Status: "done",
						}
					}
				}
			}

			var ordered []StatusInfo
			for _, key := range keys {
				ordered = append(ordered, statuses[key])
			}
			display(ordered, start, name, files)
			if done {

				return
			}
		case <-ctx.Done():
			done = true // allow ui to update once more
		}
	}
}

func display(statuses []StatusInfo, start time.Time, name string, files map[string]struct{}) {
	var total int64
	var total1 int64
	var cnt int
	downloading := false
	for _, status := range statuses {
		total += status.Offset
		total1 += status.Total

		switch status.Status {
		case "downloading", "uploading":
			cnt++
			files[status.Ref] = struct{}{}
			//msg := WsMessage{name, status.Ref, status.Status, status.Total, status.Offset, fmt.Sprintf("%8.8s/%s\t\n",
			//				progress.Bytes(status.Offset), progress.Bytes(status.Total))}
			//HandleMessages(msg)
			downloading = true
		case "resolving", "resolved":
			HandleMessages(name, logging.Message{Status: "PENDING", Ref: status.Ref, Text: status.Status})

		default:
			//msg := WsMessage{name, status.Ref, status.Status, ""}
			//HandleMessages(msg)

		}
	}
	if downloading {
		msg := logging.Message{Status: "PENDING", Ref: "Downloading", Text: "part " + strconv.Itoa(len(files)-cnt) + "/" +
			strconv.Itoa(len(files)) + " in progress: " + fmt.Sprintf("%8.8s/%s\t\n", progress.Bytes(total), progress.Bytes(total1))}
		HandleMessages(name, msg)
	} else if total == total1 && total != 0 {
		msg := logging.Message{Status: "PENDING", Ref: "Downloading", Text: "part " + strconv.Itoa(len(files)) + "/" +
			strconv.Itoa(len(files)) + " in progress: " + fmt.Sprintf("%8.8s/%s\t\n", progress.Bytes(total), progress.Bytes(total1))}
		HandleMessages(name, msg)
	}

	/*msg := WsMessage{name, "elapsed", " ", total, total, fmt.Sprintf("%-4.1fs\ttotal: %7.6v\t(%v)\t\n",
	time.Since(start).Seconds(),
	progress.Bytes(total),
	progress.NewBytesPerSecond(total, time.Since(start)))}*/
	//HandleMessages(msg)
}
