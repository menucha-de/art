/*
 * App Lifecycle Service
 *
 * This files describes the App lifecycle service
 *
 * API version: 1.0.0
 * Contact: opensource@peramic.io
 */
package swagger

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	utils "github.com/peramic/utils"
)

type Route struct {
	Name        string
	Method      string
	Pattern     string
	HandlerFunc http.HandlerFunc
}

type Routes []Route

func AddRoutes(newRoutes utils.Routes) {
	routes = append(routes, newRoutes...)
}

func NewRouter() *mux.Router {
	router := mux.NewRouter().StrictSlash(true)
	for _, route := range routes {
		var handler http.Handler
		handler = route.HandlerFunc
		handler = Logger(handler, route.Name)

		router.
			Methods(route.Method).
			Path(route.Pattern).
			Name(route.Name).
			Handler(handler)
	}

	return router
}

var routes = utils.Routes{
	utils.Route{
		"AddContainer",
		strings.ToUpper("Post"),
		"/rest/namespaces/{ns}/containers",
		AddContainer,
	},

	utils.Route{
		"Upgrade",
		strings.ToUpper("Put"),
		"/rest/namespaces/{ns}/containers",
		Upgrade,
	},

	utils.Route{
		"GetContainer",
		strings.ToUpper("Get"),
		"/rest/namespaces/{ns}/containers/{container}",
		GetContainer,
	},

	utils.Route{
		"UpdateContainer",
		strings.ToUpper("Put"),
		"/rest/namespaces/{ns}/containers/{container}",
		UpdateContainer,
	},

	utils.Route{
		"DeleteContainer",
		strings.ToUpper("Delete"),
		"/rest/namespaces/{ns}/containers/{container}",
		DeleteContainer,
	},
	utils.Route{
		"ExportContainer",
		strings.ToUpper("Get"),
		"/rest/namespaces/{ns}/containers/{container}/export",
		ExportContainer,
	},
	utils.Route{
		"GetContainers",
		strings.ToUpper("Get"),
		"/rest/namespaces/{ns}",
		GetContainers,
	},

	utils.Route{
		"GetNamespaces",
		strings.ToUpper("Get"),
		"/rest/namespaces",
		GetNamespaces,
	},
	utils.Route{
		Name:        "cors",
		Method:      "GET",
		Pattern:     "/ws",
		HandlerFunc: handleConnections,
	},
}
