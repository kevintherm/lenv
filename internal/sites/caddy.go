package sites

import (
	"encoding/json"
	"net"
	"strconv"
)

func caddyConfig(host string, port, upstream int, storage string) ([]byte, error) {
	type object = map[string]any
	loopback := func(port int) string {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	return json.Marshal(object{
		"admin":   object{"disabled": true, "config": object{"persist": false}},
		"storage": object{"module": "file_system", "root": storage},
		"apps": object{
			"pki": object{"certificate_authorities": object{
				"lenv": object{"name": "lenv Local CA", "install_trust": false},
			}},
			"tls": object{"automation": object{"policies": []object{
				{"subjects": []string{host}, "issuers": []object{{"module": "internal", "ca": "lenv"}}},
			}}},
			"http": object{"servers": object{"site": object{
				"listen":          []string{loopback(port)},
				"protocols":       []string{"h1", "h2"},
				"automatic_https": object{"disable_redirects": true},
				"tls_connection_policies": []object{
					{"match": object{"sni": []string{host}}},
				},
				"routes": []object{
					{
						"match": []object{{"host": []string{host}}},
						"handle": []object{{
							"handler": "reverse_proxy", "upstreams": []object{{"dial": loopback(upstream)}},
						}},
						"terminal": true,
					},
					{"handle": []object{{"handler": "static_response", "status_code": 404}}},
				},
			}}},
		},
	})
}
