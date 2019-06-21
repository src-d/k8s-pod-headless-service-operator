package main

import "gopkg.in/src-d/go-cli.v0"

var (
	name    = "k8s-pod-headless-service-operator"
	version = "undefined"
	build   = "undefined"
)

var app = cli.New(name, version, build, "A service to create headless services to make pod hostnames resolvable")

func main() {
	app.RunMain()
}
