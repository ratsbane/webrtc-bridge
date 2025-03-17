package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow WebSocket connections from any origin
	},
}

// Connection state constants
const (
	stateNew       = "new"
	stateOffering  = "offering"
	stateAnswering = "answering"
	stateConnected = "connected"
)

// Global video track that all connections will use
var (
	globalVideoTrack     *webrtc.TrackLocalStaticSample
	activeConnections    map[string]*webrtc.PeerConnection
	activeConnectionsMux sync.Mutex
)

func init() {
	// Create global video track
	var err error
	globalVideoTrack, err = webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:     webrtc.MimeTypeH264,
			ClockRate:    90000,
			Channels:     0,
			SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		},
		"video", 
		"pion",
	)
	if err != nil {
		log.Fatal("Error creating global video track:", err)
	}
	
	activeConnections = make(map[string]*webrtc.PeerConnection)
	
	// Start the RTP receiver
	go receiveRTP()
}

// receiveRTP listens for incoming RTP packets and forwards them to the WebRTC track
func receiveRTP() {
	// Listen on UDP port 5004
	addr := net.UDPAddr{
		Port: 5004,
		IP:   net.ParseIP("0.0.0.0"),
	}
	
	conn, err := net.ListenUDP("udp", &addr)
	if err != nil {
		log.Fatal("Error listening on UDP port 5004:", err)
	}
	defer conn.Close()
	
	// Set read buffer size to accommodate video frames
	if err := conn.SetReadBuffer(2 * 1024 * 1024); err != nil {
		log.Printf("Warning: Failed to set UDP read buffer: %v", err)
	}
	
	log.Println("Listening for RTP packets on port 5004")
	
	// Buffer for incoming packets
	buffer := make([]byte, 8192)
	
	// Variables to hold the H.264 NAL units for a frame
	var frameData []byte
	var lastSeqNum uint16
	var initialized bool
	var frameStartTime time.Time
	
	for {
		n, _, err := conn.ReadFromUDP(buffer)
		if err != nil {
			log.Println("Error reading UDP packet:", err)
			continue
		}
		
		// Parse RTP packet
		rtpPacket := &rtp.Packet{}
		if err := rtpPacket.Unmarshal(buffer[:n]); err != nil {
			log.Println("Error parsing RTP packet:", err)
			continue
		}
		
		// Check for sequence continuity
		if initialized && rtpPacket.SequenceNumber != (lastSeqNum+1)&0xFFFF {
			log.Printf("RTP packet loss detected: got %d, expected %d", rtpPacket.SequenceNumber, (lastSeqNum+1)&0xFFFF)
			// Reset frame data on packet loss to avoid corrupted frames
			frameData = nil
		}
		
		initialized = true
		lastSeqNum = rtpPacket.SequenceNumber
		
		// Record the timestamp of the first packet in a frame
		if len(frameData) == 0 {
			frameStartTime = time.Now()
		}
		
		// For H.264 over RTP:
		// 1. Extract the payload
		payload := rtpPacket.Payload
		
		// 2. Process the H.264 NAL units
		if len(payload) > 0 {
			// Get the NAL unit type from the first byte
			nalType := payload[0] & 0x1F
			
			// RFC 6184 defines how H.264 is packetized in RTP
			switch nalType {
			case 28: // FU-A fragmentation unit
				// Handle fragmented NAL units
				if len(payload) < 2 {
					continue // Invalid FU-A packet
				}
				
				// Check if this is the start of a fragmented NAL unit
				if payload[1]&0x80 != 0 { // Start bit set
					// Reconstruct the original NAL header
					originalNalHeader := (payload[0] & 0xE0) | (payload[1] & 0x1F)
					
					// Add NAL unit delimiter for fragment start
					frameData = append(frameData, 0x00, 0x00, 0x00, 0x01)
					frameData = append(frameData, originalNalHeader)
					
					// Add the rest of the fragment payload (skipping the FU header)
					if len(payload) > 2 {
						frameData = append(frameData, payload[2:]...)
					}
				} else {
					// Middle or end fragment, just add the payload (skipping the FU header)
					if len(payload) > 2 {
						frameData = append(frameData, payload[2:]...)
					}
				}
				
			default: // Single NAL unit
				// Add NAL unit delimiter
				frameData = append(frameData, 0x00, 0x00, 0x00, 0x01)
				// Then add the complete NAL unit
				frameData = append(frameData, payload...)
			}
		}
		
		// 3. If marker bit is set, this is the last packet of the frame
		if rtpPacket.Marker {
			// We've received a complete frame
			frameDuration := time.Since(frameStartTime)
			
			// Log frame statistics periodically
			if rtpPacket.SequenceNumber%300 == 0 {
				log.Printf("Complete frame received: %d bytes, took %v", len(frameData), frameDuration)
			}
			
			// Send the frame to WebRTC only if we have data and it's not too small to be valid
			if len(frameData) > 10 {
				sample := media.Sample{
					Data:     frameData,
					Duration: time.Millisecond * 33, // Fixed 30fps duration for smoother playback
				}
				
				// Check if we have any active connections before trying to write
				activeConnectionsMux.Lock()
				hasConnections := len(activeConnections) > 0
				activeConnectionsMux.Unlock()
				
				if hasConnections {
					if err := globalVideoTrack.WriteSample(sample); err != nil {
						log.Println("Error writing sample to track:", err)
					}
				}
			} else if len(frameData) > 0 {
				log.Printf("Warning: Discarding suspiciously small frame of %d bytes", len(frameData))
			}
			
			// Reset for next frame
			frameData = nil
		}
	}
}

// isNewNALU checks if this byte indicates a new NAL Unit
func isNewNALU(b byte) bool {
	// In H.264, NAL unit types are identified by the first byte
	// This is a simplified check - in a production system,
	// you might want to do more sophisticated H.264 parsing
	nalType := b & 0x1F // Extract NAL unit type (5 bits)
	return nalType >= 1 && nalType <= 23 // Most common NAL unit types
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()
	
	// Generate a unique ID for this connection
	connectionID := fmt.Sprintf("%p", conn)
	fmt.Println("WebSocket client connected:", connectionID)

	// Add a mutex for WebSocket writes to prevent concurrent access
	var wsWriteMutex sync.Mutex

	// Create a new WebRTC peer connection
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		log.Println("Error creating peer connection:", err)
		return
	}
	
	// Register this connection
	activeConnectionsMux.Lock()
	activeConnections[connectionID] = peerConnection
	activeConnectionsMux.Unlock()
	
	// Make sure to clean up when done
	defer func() {
		peerConnection.Close()
		activeConnectionsMux.Lock()
		delete(activeConnections, connectionID)
		activeConnectionsMux.Unlock()
		log.Println("Connection closed:", connectionID)
	}()

	// Add connection state tracking
	connectionState := stateNew
	var stateLock sync.Mutex
	
	// Create a safe write function to prevent concurrent writes
	safeWrite := func(data interface{}) error {
		wsWriteMutex.Lock()
		defer wsWriteMutex.Unlock()
		return conn.WriteJSON(data)
	}

	// Handle incoming ICE candidates from the client
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate != nil {
			resp := map[string]interface{}{
				"type":      "ice-candidate",
				"candidate": candidate.ToJSON(),
			}
			// Use safe write to avoid concurrent access
			go func() {
				wsWriteMutex.Lock()
				defer wsWriteMutex.Unlock()
				
				// Check if connection is still open before writing
				if err := conn.WriteJSON(resp); err != nil {
					log.Println("Error sending ICE candidate:", err)
					return
				}
				fmt.Println("Sent ICE Candidate to client:", connectionID)
			}()
		}
	})

	// Handle connection state changes
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		fmt.Printf("Connection %s state changed to: %s\n", connectionID, state.String())
		
		if state == webrtc.PeerConnectionStateFailed {
			log.Println("Connection failed:", connectionID)
		}
		
		if state == webrtc.PeerConnectionStateConnected {
			stateLock.Lock()
			connectionState = stateConnected
			stateLock.Unlock()
			log.Printf("Client %s connected and receiving video", connectionID)
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
			sendErrorResponse(safeWrite, "invalid-format", "Invalid JSON format")
			continue
		}

		msgType, ok := request["type"].(string)
		if !ok {
			log.Println("Missing or invalid 'type' field")
			sendErrorResponse(safeWrite, "invalid-message", "Missing or invalid 'type' field")
			continue
		}

		switch msgType {
		case "offer":
			fmt.Println("Received connection request from:", connectionID)
			
			// Update connection state
			stateLock.Lock()
			if connectionState == stateOffering {
				log.Println("Already in offering state, ignoring duplicate offer")
				stateLock.Unlock()
				continue
			}
			connectionState = stateOffering
			stateLock.Unlock()
			
			// Add the global video track to this peer connection
			sender, err := peerConnection.AddTrack(globalVideoTrack)
			if err != nil {
				log.Println("Error adding track:", err)
				sendErrorResponse(safeWrite, "track-add-error", "Error adding video track")
				continue
			}
			
			// Start a goroutine to read RTCP packets for this sender
			go func() {
				rtcpBuf := make([]byte, 1500)
				for {
					if _, _, rtcpErr := sender.Read(rtcpBuf); rtcpErr != nil {
						return
					}
				}
			}()
			
			// Create our own offer
			var offer webrtc.SessionDescription
			offer, err = peerConnection.CreateOffer(nil)
			if err != nil {
				log.Println("Error creating offer:", err)
				sendErrorResponse(safeWrite, "create-offer-error", "Error creating SDP offer")
				continue
			}

			err = peerConnection.SetLocalDescription(offer)
			if err != nil {
				log.Println("Error setting local description:", err)
				sendErrorResponse(safeWrite, "local-sdp-error", "Error setting local SDP")
				continue
			}

			response := map[string]string{"type": "offer", "sdp": offer.SDP}
			if err := safeWrite(response); err != nil {
				log.Println("Error sending SDP offer:", err)
				continue
			}
			fmt.Println("Sent SDP offer to:", connectionID)

		case "answer":
			fmt.Println("Received SDP Answer from:", connectionID)
			
			// Check if we're in the right state to receive an answer
			stateLock.Lock()
			if connectionState != stateOffering {
				log.Println("Ignoring answer: not in offering state")
				sendErrorResponse(safeWrite, "invalid-state", "Cannot process answer in current state")
				stateLock.Unlock()
				continue
			}
			connectionState = stateAnswering
			stateLock.Unlock()
			
			sdpAnswer, ok := request["sdp"].(string)
			if !ok {
				log.Println("Error: SDP answer is not a string or is missing")
				sendErrorResponse(safeWrite, "invalid-answer", "SDP answer is not a string or is missing")
				continue
			}
			
			err = peerConnection.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdpAnswer})
			if err != nil {
				log.Println("Error setting remote description:", err)
				sendErrorResponse(safeWrite, "remote-sdp-error", "Error setting remote SDP")
				continue
			}
			fmt.Println("Successfully set SDP Answer for:", connectionID)

		case "ice-candidate":
			fmt.Println("Received ICE Candidate from client:", connectionID)
			
			// Check if we have valid connection first
			if peerConnection.ConnectionState() == webrtc.PeerConnectionStateClosed {
				log.Println("Ignoring ICE candidate: connection closed")
				continue
			}
			
			candidateData, ok := request["candidate"].(map[string]interface{})
			if !ok {
				// Check if it's possibly a null candidate (end of candidates)
				if request["candidate"] == nil {
					fmt.Println("Received end-of-candidates indicator")
					continue
				}
				log.Println("Error: ICE candidate is not in expected format")
				sendErrorResponse(safeWrite, "invalid-candidate", "ICE candidate is not in expected format")
				continue
			}
			
			candidateJSON, err := json.Marshal(candidateData)
			if err != nil {
				log.Println("Error marshalling ICE candidate:", err)
				sendErrorResponse(safeWrite, "candidate-format-error", "Error processing ICE candidate")
				continue
			}
			
			var candidate webrtc.ICECandidateInit
			err = json.Unmarshal(candidateJSON, &candidate)
			if err != nil {
				log.Println("Error parsing ICE candidate:", err)
				sendErrorResponse(safeWrite, "candidate-parse-error", "Error parsing ICE candidate")
				continue
			}
			
			err = peerConnection.AddICECandidate(candidate)
			if err != nil {
				log.Println("Error adding ICE candidate:", err)
				sendErrorResponse(safeWrite, "add-candidate-error", "Error adding ICE candidate")
				continue
			}
			fmt.Println("Successfully added ICE Candidate for:", connectionID)
			
		default:
			log.Printf("Unknown message type: %s\n", msgType)
			sendErrorResponse(safeWrite, "unknown-type", fmt.Sprintf("Unknown message type: %s", msgType))
		}
	}
}

// sendErrorResponse sends structured error information to the client
func sendErrorResponse(writeFunc func(interface{}) error, errorType string, errorMsg string) {
	response := map[string]string{
		"type":    "error",
		"error":   errorType,
		"message": errorMsg,
	}
	if err := writeFunc(response); err != nil {
		log.Println("Error sending error response:", err)
	}
}

func main() {
	// Create directory for any temporary files
	os.MkdirAll("tmp", 0755)
	
	http.HandleFunc("/ws", handleWebSocket)
	
	port := 8080
	fmt.Printf("WebSocket server running on ws://localhost:%d/ws\n", port)
	fmt.Println("Directly receiving RTP H.264 stream on port 5004")
	log.Fatal(http.ListenAndServe(fmt.Sprintf("0.0.0.0:%d", port), nil))
}
