/*
 * Container Lifecycle Service
 *
 * This files describes the container lifecycle service
 *
 * API version: 1.0.0
 * Contact: info@menucha.de
 */
package containers

type Container struct {
	Id string `json:"id,omitempty"`

	Name string `json:"name,omitempty"`

	Label string `json:"label,omitempty"`

	Image string `json:"image,omitempty"`

	User string `json:"user,omitempty"`

	Passwd string `json:"passwd,omitempty"`

	Trust bool `json:"trust,omitempty"`

	State string `json:"state,omitempty"`

	Namespaces []Namespace `json:"namespaces,omitempty"`

	Devices []Device `json:"devices,omitempty"`

	Mounts []Mount `json:"mounts,omitempty"`

	Capabilities []string `json:"capabilities,omitempty"`

	//Additional Infos
	IsActive  bool   `json:"isactive,omitempty"`
	IsHostNet bool   `json:"ishostnet,omitempty"`
	Version   string `json:"version,omitempty"`
}

const (
	Starting = "STARTING"
	Started  = "STARTED"
	Stopping = "STOPPING"
	Stopped  = "STOPPED"
)
