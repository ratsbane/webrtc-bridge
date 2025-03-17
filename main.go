package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/pion/webrtc/v3"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow WebSocket connections from any origin
	},
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	fmt.Println("WebSocket client connected")

	// Create a new WebRTC peer connection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Println("Error creating peer connection:", err)
		return
	}

	// Handle incoming ICE candidates from the client
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			resp := map[string]interface{}{
				"type":     "ice-candidate",
				"candidate": candidate.ToJSON(),
			}
			conn.WriteJSON(resp)
			fmt.Println("Sent ICE Candidate to client:", resp)
		}
	})

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			log.Println("WebSocket read error:", err)
			break
		}

		var request map[string]interface{}
		if err := json.Unmarshal(message, &request); err != nil {
			log.Println("Invalid JSON:", err)
			continue
		}

		switch request["type"] {
		case "offer":
			fmt.Println("Received SDP Offer")
			videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "example")
			if err != nil {
				log.Println("Error creating video track:", err)
				return
			}
			_, err = peerConnection.AddTrack(videoTrack)
			if err != nil {
				log.Println("Error adding track:", err)
				return
			}

			offer, err := peerConnection.CreateOffer(nil)
			if err != nil {
				log.Println("Error creating offer:", err)
				return
			}
			err = peerConnection.SetLocalDescription(offer)
			if err != nil {
				log.Println("Error setting local description:", err)
				return
			}

			response := map[string]string{"type": "offer", "sdp": offer.SDP}
			conn.WriteJSON(response)
			fmt.Println("Sent SDP offer")

		case "answer":
			fmt.Println("Received SDP Answer")
			answer, ok := request["sdp"].(string)
			if !ok {
				log.Println("Error: SDP answer is not a string")
				return
			}
			err = peerConnection.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer})
			if err != nil {
				log.Println("Error setting remote description:", err)
				return
			}
			fmt.Println("Successfully set SDP Answer")

		case "ice-candidate":
			fmt.Println("Received ICE Candidate from client")
			candidateData, ok := request["candidate"].(map[string]interface{})
			if !ok {
				log.Println("Error: ICE candidate is not in expected format")
				return
			}

			candidateJSON, err := json.Marshal(candidateData)
			if err != nil {
				log.Println("Error marshalling ICE candidate:", err)
				return
			}

			var candidate webrtc.ICECandidateInit
			err = json.Unmarshal(candidateJSON, &candidate)
			if err != nil {
				log.Println("Error parsing ICE candidate:", err)
				return
			}

			err = peerConnection.AddICECandidate(candidate)
			if err != nil {
				log.Println("Error adding ICE candidate:", err)
				return
			}
			fmt.Println("Successfully added ICE Candidate:", candidate.Candidate)
		}
	}
}

func main() {
	http.HandleFunc("/ws", handleWebSocket)
	fmt.Println("WebSocket server running on ws://localhost:8080/ws")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
}

