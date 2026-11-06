package state

import "encoding/json"

// ListSnapshots exposes immutable observations in event order. Current selects
// the most recent observation per activation and output name; history includes
// earlier attempts. Neither mode reads or changes committed activation outputs.
func ListSnapshots(store Store, runID, activationID, attemptID string, current bool) ([]Event, error) {
	result := make([]Event, 0)
	latest := map[string]int{}
	after := 0
	for {
		page, err := store.GetEvents(runID, after, 500)
		if err != nil {
			return nil, err
		}
		for _, event := range page {
			after = event.Seq
			if event.Type != EventArtifactSnapshot {
				continue
			}
			if activationID != "" && event.Payload["activationId"] != activationID {
				continue
			}
			if attemptID != "" && event.SubjectID != attemptID {
				continue
			}
			var snapshot ArtifactSnapshot
			raw, err := json.Marshal(event.Payload["snapshot"])
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				return nil, err
			}
			activation, _ := event.Payload["activationId"].(string)
			key := activation + "\x00" + snapshot.Name
			if previous, ok := latest[key]; current && ok {
				result[previous].Type = ""
			}
			latest[key] = len(result)
			result = append(result, event)
		}
		if len(page) < 500 {
			break
		}
	}
	filtered := result[:0]
	for _, event := range result {
		if event.Type != "" {
			filtered = append(filtered, event)
		}
	}
	return filtered, nil
}
