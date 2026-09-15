package services

import (
	"testing"

	"t3z/api-gateway/internal/config"
)

func TestWindmillService_LiveConnection(t *testing.T) {
	cfg := &config.Config{
		WindmillBaseURL:   "http://127.0.0.1:3001",
		WindmillWorkspace: "t3z",
		WindmillToken:     "wm_t3z_gateway_2026_supertoken",
	}

	wm := NewWindmillService(cfg)
	testFolder := "test_ci_folder"

	err := wm.CreateFolder(testFolder)
	if err != nil {
		t.Fatalf("CreateFolder failed: %v", err)
	}

	// Deleting test folder
	wm.DeleteFolder(testFolder)
}
