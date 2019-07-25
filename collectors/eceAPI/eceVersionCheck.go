package eceAPI

import (
	"context"
	"time"

	"github.com/elastic/ece-support-diagnostics/helpers"
	"github.com/tidwall/gjson"
)

func ECEversionCheck(taskCtx helpers.TaskContext) string {
	taskCtx.Version = "0"
	taskCtx.Task = helpers.Task{
		Filename: "ece/platform.json",
		Uri:      "/api/v1/platform",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	responseBytes, err := taskCtx.DoRequest(ctx)
	if err != nil {
		// something went wrong here, maybe exit?
	}
	platformVersion := gjson.GetBytes(responseBytes, "version")
	return platformVersion.String()
}
