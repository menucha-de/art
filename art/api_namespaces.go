/*
 * App Lifecycle Service
 *
 * This files describes the App lifecycle service
 *
 * API version: 1.0.0
 * Contact: info@menucha.de
 */
package art

import (
	"encoding/json"
	"net/http"

	"github.com/menucha-de/art/art/containers"
)

// GetNamespaces gets all available namespaces
func GetNamespaces(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	var err = json.NewEncoder(w).Encode(containers.GetNamespaces())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	//w.WriteHeader(http.StatusOK)
}

// Service ...
type Service int

// GetNamespaces ...
func (s *Service) GetNamespaces(req int, resp *[2]string) error {
	*resp = containers.GetNamespaces()
	return nil
}
