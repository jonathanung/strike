package provider

import "errors"

// authFailureMarker is implemented by errors that know the provider rejected
// the saved credential (vendor 401 and similar). Used by HTTP status errors
// from base (#1546).
type authFailureMarker interface {
	AuthFailure() (providerName string, ok bool)
}

// AuthFailure reports whether err is a provider credential rejection and, if
// so, which provider rejected it. Classification lives with the adapter that
// saw the HTTP status; plain errors are never classified from their text.
func AuthFailure(err error) (providerName string, ok bool) {
	if err == nil {
		return "", false
	}
	var marker authFailureMarker
	if errors.As(err, &marker) {
		return marker.AuthFailure()
	}
	return "", false
}
