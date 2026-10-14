package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
)

type InstalledModel struct {
	Name    string `json:"name"`
	Digest  string `json:"digest"`
	Size    int64  `json:"size"`
	Details struct {
		Family        string `json:"family"`
		ParameterSize string `json:"parameter_size"`
		Quantization  string `json:"quantization_level"`
	} `json:"details"`
}

func (c Client) Installed(ctx context.Context) ([]InstalledModel, error) {
	response, err := c.request(ctx, http.MethodGet, "/api/tags", nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var body struct {
		Models []InstalledModel `json:"models"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body)
	return body.Models, err
}
