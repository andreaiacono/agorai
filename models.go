package main

// ModelOption is a selectable cloud model. ID is passed to `claude --model`
// ("" means the user's configured default). We use the opus/sonnet/haiku
// aliases so they track the latest version without hardcoding model ids.
type ModelOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var models = []ModelOption{
	{ID: "", Label: "Default"},
	{ID: "opus", Label: "Opus"},
	{ID: "sonnet", Label: "Sonnet"},
	{ID: "haiku", Label: "Haiku"},
}

// modelAllowed guards against arbitrary strings reaching the claude CLI args.
func modelAllowed(id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}

func modelArgs(id string) []string {
	if id == "" {
		return nil
	}
	return []string{"--model", id}
}

func modelLabel(id string) string {
	if id == "" {
		return "default"
	}
	for _, m := range models {
		if m.ID == id {
			return m.Label
		}
	}
	return id
}
