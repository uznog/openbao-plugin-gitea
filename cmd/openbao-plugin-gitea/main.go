package main

import (
	"os"

	hclog "github.com/hashicorp/go-hclog"
	"github.com/openbao/openbao/sdk/v2/plugin"

	gitea "github.com/uznog/openbao-plugin-gitea"
)

func main() {
	if err := plugin.ServeMultiplex(&plugin.ServeOpts{BackendFactoryFunc: gitea.Factory}); err != nil {
		hclog.New(&hclog.LoggerOptions{}).Error("plugin shutting down", "error", err)
		os.Exit(1)
	}
}
