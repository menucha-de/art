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
	"net/http"
)

func init() {
	//log.SetLevel(logrus.ErrorLevel)
}

func Logger(inner http.Handler, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		// log.Printf(
		// 	"%s %s %s %s",
		// 	r.Method,
		// 	r.RequestURI,
		// 	name,
		// 	time.Since(start),
		// )
	})
}
