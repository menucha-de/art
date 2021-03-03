/*
 * Container Lifecycle Service
 *
 * This files describes the container lifecycle service
 *
 * API version: 1.0.0
 * Contact: opensource@peramic.io
 */
package containers

type Mount struct {
	Type_ string `json:"type,omitempty"`

	Source string `json:"source,omitempty"`

	Destination string `json:"destination,omitempty"`

	Options []string `json:"options,omitempty"`
}
