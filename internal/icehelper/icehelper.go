// Package icehelper bridges browser-native ICE wire formats (as delivered
// over signaling) and pion's typed representations. Both the listener side
// (internal/peer) and the connector side (internal/client) reach for the
// same three conversions — parsing the server's ice_servers offer, parsing
// an inbound trickle candidate from the peer, and serializing a locally-
// gathered candidate for transmission — so they live here once.
package icehelper

import (
	"encoding/json"
	"fmt"
	"github.com/pion/webrtc/v4"
	"strings"
)

// Takes an array of []any and converts to ICEServers, if possible.
func AnyToICEServers(raw []any) []webrtc.ICEServer {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}

	return parseICEServers(data)

}

// ParseICEServers reads the "ice_servers" field of a signaling message
// and returns it as pion's []webrtc.ICEServer. The input is the full
// message (map[string]interface{}); a missing or malformed ice_servers
// returns nil — callers that need the empty/missing distinction should
// check msg["ice_servers"] themselves.
//
// The browser-native wire format allows urls to be either a string or
// a []string; both are accepted. A Username triggers password-credential
// type (the only one pion supports for trickle ICE).
func parseICEServers(raw []byte) []webrtc.ICEServer {

	var entries []struct {
		URLs       interface{} `json:"urls"`
		Username   string      `json:"username"`
		Credential string      `json:"credential"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	var out []webrtc.ICEServer
	for _, e := range entries {
		var urls []string
		switch v := e.URLs.(type) {
		case string:
			urls = []string{v}
		case []interface{}:
			for _, u := range v {
				if s, ok := u.(string); ok {
					urls = append(urls, s)
				}
			}
		}
		s := webrtc.ICEServer{URLs: urls}
		if e.Username != "" {
			s.Username = e.Username
			s.Credential = e.Credential
			s.CredentialType = webrtc.ICECredentialTypePassword
		}
		out = append(out, s)
	}
	return out
}

// CandidateInit converts a JSON-decoded RTCIceCandidate-shaped object
// (as sent by browsers via signaling) to pion's init form. Returns
// ok=false for the empty/end-of-candidates marker so callers can no-op
// instead of forwarding it to pion.
func CandidateInit(candidateData map[string]interface{}) (webrtc.ICECandidateInit, bool) {
	candidateStr, _ := candidateData["candidate"].(string)
	if candidateStr == "" {
		return webrtc.ICECandidateInit{}, false
	}
	sdpMid, _ := candidateData["sdpMid"].(string)
	sdpMLineIndexFloat, _ := candidateData["sdpMLineIndex"].(float64)
	sdpMLineIndex := uint16(sdpMLineIndexFloat)
	return webrtc.ICECandidateInit{
		Candidate:     candidateStr,
		SDPMid:        &sdpMid,
		SDPMLineIndex: &sdpMLineIndex,
	}, true
}

// CandidateMap converts a pion locally-gathered candidate to the JSON-
// shaped map the browser expects on the wire (matching
// RTCIceCandidate.toJSON()). The signaling layer ships it verbatim
// inside the candidate field of a "candidate" message.
func CandidateMap(c *webrtc.ICECandidate) map[string]interface{} {
	j := c.ToJSON()
	return map[string]interface{}{
		"candidate":     j.Candidate,
		"sdpMid":        j.SDPMid,
		"sdpMLineIndex": j.SDPMLineIndex,
	}
}

func UnMarshalUserIceJson(data []byte) ([]webrtc.ICEServer, error) {

	var parsed []webrtc.ICEServer

	// Clean up leading whitespace to check the root structural token safely
	trimmed := strings.TrimSpace(string(data))
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty ICE config file. ")
	}

	// Case 3: The payload is a direct raw array -> [{...}]
	if trimmed[0] == '[' {

		if parsed = parseICEServers(data); parsed == nil {
			return nil, fmt.Errorf("Not a valid ICE config file.")
		}

		return parsed, nil
	}

	// Case 1 & 2: The payload is an object wrapper -> {"ice_servers": ...} or {"iceServers": ...}
	if trimmed[0] == '{' {
		var wrapper map[string]json.RawMessage
		// Use json.RawMessage to hold keys without decoding their bodies yet
		if err := json.Unmarshal(data, &wrapper); err != nil {
			return nil, err
		}

		// Look for either key variation dynamically
		if rawArray, exists := wrapper["ice_servers"]; exists {

			if parsed = parseICEServers(rawArray); parsed == nil {
				return nil, fmt.Errorf("ice_servers JSON node did not parse to an array of ICE configs.")
			}

			return parsed, nil

		}
		if rawArray, exists := wrapper["iceServers"]; exists {

			if parsed = parseICEServers(rawArray); parsed == nil {
				return nil, fmt.Errorf("iceServers JSON node did not parse to an array of ICE configs.")
			}

			return parsed, nil

		}
	}

	// Fail cleanly if the payload format does not match any expected structure
	return nil, fmt.Errorf("unexpected JSON format for ICE servers configuration")
}
