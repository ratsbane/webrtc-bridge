package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/pion/webrtc/v3"
)

var peerConnection *webrtc.PeerConnection

func main() {
	// Create a new peer connection
	var err error
	peerConnection, err = webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Fatal(err)
	}

	// Add a video track to the peer connection
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "example")
	if err != nil {
		log.Fatal(err)
	}
	_, err = peerConnection.AddTrack(videoTrack)
	if err != nil {
		log.Fatal(err)
	}

	// Start HTTP server
	http.HandleFunc("/offer", handleSDPOffer)
	fmt.Println("Server is running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}


func handleSDPOffer(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Received request for SDP Offer")

	// Allow cross-origin requests
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle preflight requests
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Create a new peer connection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		log.Println("Error creating peer connection:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Add video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264}, "video", "example")
	if err != nil {
		log.Println("Error creating video track:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_, err = peerConnection.AddTrack(videoTrack)
	if err != nil {
		log.Println("Error adding track:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create an SDP offer
	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		log.Println("Error creating offer:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Set the local description
	err = peerConnection.SetLocalDescription(offer)
	if err != nil {
		log.Println("Error setting local description:", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return SDP as JSON
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"sdp": offer.SDP})

	fmt.Println("Successfully generated SDP offer")
}




