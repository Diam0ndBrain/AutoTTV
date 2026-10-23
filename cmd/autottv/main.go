package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"autoTtv/internal/browser"
	"autoTtv/internal/twitch"
	"autoTtv/internal/web"
)

func main() {
	// Parse command line arguments
	port := flag.String("port", "3000", "Port to serve the management dashboard and OAuth callback")
	flag.Parse()

	fmt.Println("========================================")
	fmt.Println("            AutoTTV Starting            ")
	fmt.Println("[AutoTTV] Web dashboard auto-start enabled. OAuth fully integrated.")
	fmt.Println("========================================")

	// 1. Initialize browser manager
	processMgr := browser.NewProcessManager()
	browserMgr := browser.NewManager(processMgr)

	// 2. Initialize web server
	server := web.NewServer(*port, browserMgr)

	// 3. Start web server in background
	go func() {
		if err := server.Start(); err != nil {
			log.Fatalf("Web dashboard server failed: %v", err)
		}
	}()

	// 4. Set up OS signal listener for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Keep track of the active websocket manager
	var activeWSMgr *twitch.WebSocketManager

	// 5. Main event loop
	fmt.Println("[AutoTTV] Ready. Waiting for Twitch authentication and configuration...")

	for {
		select {
		case <-server.GetChannelUpdateChan():
			ids := server.GetMonitoredIDs()
			twitchClient := server.GetTwitchClient()

			if twitchClient == nil || len(ids) == 0 {
				if activeWSMgr != nil {
					fmt.Println("[AutoTTV] No monitored channels. Stopping EventSub.")
					activeWSMgr.Stop()
					activeWSMgr = nil
					server.SetWSManager(nil)
				}
				continue
			}

			fmt.Printf("[AutoTTV] Monitoring %d channel(s): %v\n", len(ids), ids)

			// Manage WebSocket subscription
			if activeWSMgr != nil {
				fmt.Printf("[AutoTTV] Updating active WebSocket subscriptions to channel IDs: %v...\n", ids)
				if err := activeWSMgr.SetBroadcasterIDs(ids); err != nil {
					fmt.Printf("[AutoTTV] Failed to update WebSocket broadcaster targets: %v\n", err)
				}
			} else {
				fmt.Printf("[AutoTTV] Starting new EventSub WebSocket for channel IDs: %v...\n", ids)
				activeWSMgr = twitch.NewWebSocketManager(twitchClient, ids, server.OnStreamOnline, server.OnStreamOffline)
				activeWSMgr.OnResubscribed = server.ResyncLiveStatus
				server.SetWSManager(activeWSMgr)

				// Run connection in background
				go func(wm *twitch.WebSocketManager) {
					if err := wm.Start(); err != nil {
						fmt.Printf("[AutoTTV] WebSocket connection ended/failed: %v\n", err)
					}
				}(activeWSMgr)
			}

		case <-server.GetLogoutChan():
			fmt.Println("[AutoTTV] Logout requested. Clearing monitoring and stopping WebSocket...")
			if activeWSMgr != nil {
				activeWSMgr.Stop()
				activeWSMgr = nil
				server.SetWSManager(nil)
			}
			if err := browserMgr.CloseStream(); err != nil {
				fmt.Printf("[AutoTTV] Error closing browser on logout: %v\n", err)
			}

		case sig := <-sigChan:
			fmt.Printf("\n[AutoTTV] Received signal %v. Initiating graceful shutdown...\n", sig)
			if activeWSMgr != nil {
				activeWSMgr.Stop()
			}
			if err := browserMgr.CloseStream(); err != nil {
				fmt.Printf("[AutoTTV] Error closing browser on shutdown: %v\n", err)
			}
			fmt.Println("[AutoTTV] Shutdown complete. Goodbye!")
			os.Exit(0)
		}
	}
}
