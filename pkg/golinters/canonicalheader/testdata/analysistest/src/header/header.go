package header

import "net/http"

func handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")

	headers := make(http.Header)

	headers.Add("X-PrettyPrint", "1") // want `use "X-Prettyprint" instead of "X-PrettyPrint"`

	_ = r.Header.Get("Userled-API-Key") // want `use "Userled-Api-Key" instead of "Userled-API-Key"`
}
