package moderation

import (
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"

	"github.com/gin-gonic/gin"
)

const (
	roleSystem = "system" // system, developer and instructions
	roleUser   = "user"
)

// segment is the text of one message (its text parts joined by "\n") that
// the attempt's upstream body will carry.
type segment struct {
	role  string
	text  string
	order int // position in request order
	norm  *normalized
}

// normalize returns seg normalised, normalising it on first use within the
// extraction's normalisation budget (see normalizeBudgetBytes). nil means the
// budget is spent: seg is neither checked nor scanned.
func (ex *extraction) normalize(seg *segment) *normalized {
	if seg.norm != nil {
		return seg.norm
	}
	if ex.normalizeStopped || ex.normalizeUsed+len(seg.text) > normalizeBudgetBytes {
		ex.normalizeStopped = true
		return nil
	}
	seg.norm = normalizeSegment(seg.text)
	ex.normalizeUsed += max(len(seg.text), len(seg.norm.text))
	return seg.norm
}

type imageRef struct {
	url   string
	order int
}

// extraction is what content moderation checks of a request on one
// conversion path: system and user text and user images, never assistant
// output, tool output or tool definitions.
type extraction struct {
	segments       []*segment
	images         []imageRef
	uncheckedParts int
	unsupported    string // ModerationDetailTokenPrompt or ModerationDetailAmbiguousKeys
	truncated      bool   // a segment was longer than segmentMaxBytes before normalisation

	normalizeUsed    int  // bytes charged against normalizeBudgetBytes
	normalizeStopped bool // a segment did not fit the budget: none after it is normalised

	scan          []scanText // the rule scan, built once
	scanBuilt     bool
	scanTruncated bool
	ruleResults   map[string]*ruleHit // rule key -> first hit (nil: none)
}

type extractor struct {
	ex    *extraction
	order int
}

func (e *extractor) add(role, text string) {
	if text == "" {
		return
	}
	if len(text) > segmentMaxBytes {
		text, _ = cutHeadTail(text, segmentMaxBytes)
		e.ex.truncated = true
	}
	e.ex.segments = append(e.ex.segments, &segment{role: role, text: text, order: e.order})
	e.order++
}

// image records an image of a system or user message (an upstream reads
// both).
func (e *extractor) image(url string) {
	if url == "" {
		return
	}
	e.ex.images = append(e.ex.images, imageRef{url: url, order: e.order})
	e.order++
}

func (e *extractor) unchecked() { e.ex.uncheckedParts++ }

func (e *extractor) fail(detail string) {
	if e.ex.unsupported == "" {
		e.ex.unsupported = detail
	}
}

// roleClass sorts a message role: system and developer are system text,
// every other role (unknown, empty, differently cased) is read as user text,
// as an OpenAI-compatible upstream may.
func roleClass(role string) string {
	switch strings.TrimSpace(role) {
	case "system", "developer":
		return roleSystem
	}
	return roleUser
}

// extractRequest maps info.Request onto what the attempt sends upstream.
// viaResponses is the chat or Claude request converted to Responses.
// handled is false for a request type it does not know.
func extractRequest(c *gin.Context, info *relaycommon.RelayInfo, viaResponses, passthrough bool) (ex *extraction, handled bool) {
	e := &extractor{ex: &extraction{}}
	switch req := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		e.chat(req, viaResponses)
	case *dto.OpenAIResponsesRequest:
		e.responses(req.Input, req.Instructions, req.Prompt)
	case *dto.OpenAIResponsesCompactionRequest:
		e.responses(req.Input, req.Instructions, nil)
	case *dto.ClaudeRequest:
		if passthrough {
			// An OpenAI channel sends the raw body to /v1/chat/completions,
			// which reads it as a chat body; only the top-level system is
			// Claude's own.
			e.claudeSystem(req, false)
			e.passthroughChat(c)
		} else {
			e.claude(req, viaResponses)
		}
	case *dto.GeminiChatRequest:
		e.gemini(req)
		if passthrough {
			// The raw body goes to /v1/chat/completions too, where a lenient
			// upstream may read messages next to contents.
			e.passthroughChat(c)
		}
	case *dto.ImageRequest:
		if c != nil && c.Request != nil && c.Request.MultipartForm != nil && len(c.Request.MultipartForm.Value["prompt"]) > 0 {
			for _, prompt := range c.Request.MultipartForm.Value["prompt"] {
				e.add(roleUser, prompt)
			}
		} else {
			e.add(roleUser, req.Prompt)
		}
	case *dto.AudioRequest: // speech; transcriptions skip moderation
		e.add(roleSystem, req.Instructions)
		e.add(roleUser, req.Input)
	default:
		return nil, false
	}
	if passthrough && e.ex.unsupported == "" {
		switch info.Request.(type) {
		case *dto.GeneralOpenAIRequest, *dto.OpenAIResponsesRequest, *dto.OpenAIResponsesCompactionRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest, *dto.ImageRequest:
			if passthroughBodyIsAmbiguous(c) {
				e.fail(relaycommon.ModerationDetailAmbiguousKeys)
			}
		}
	}
	return e.ex, true
}

// extractTask reads the prompt of a task submission.
func extractTask(c *gin.Context) *extraction {
	e := &extractor{ex: &extraction{}}
	value, _ := c.Get("task_request")
	switch req := value.(type) {
	case map[string]any:
		if prompt, ok := req["prompt"].(string); ok {
			e.add(roleUser, prompt)
		}
	case relaycommon.TaskSubmitReq:
		e.add(roleUser, req.Prompt)
	case *relaycommon.TaskSubmitReq:
		if req != nil {
			e.add(roleUser, req.Prompt)
		}
	}
	return e.ex
}

func (e *extractor) chat(req *dto.GeneralOpenAIRequest, viaResponses bool) {
	for i := range req.Messages {
		msg := &req.Messages[i]
		r := strings.TrimSpace(msg.Role)
		switch {
		case msg.Role == "assistant" || viaResponses && r == "assistant":
			continue
		case r == "tool" || r == "function":
			// The Responses conversion turns a tool message without a call
			// id into user text.
			if viaResponses && strings.TrimSpace(msg.ToolCallId) == "" {
				e.add(roleUser, missingCallIDText(msg))
			}
			continue
		case viaResponses && r == "":
			continue
		}
		e.chatContent(roleClass(msg.Role), msg.Content)
	}
	switch prompt := req.Prompt.(type) {
	case nil:
	case string:
		e.add(roleUser, prompt)
	case []any:
		for _, item := range prompt {
			if _, ok := item.(string); !ok {
				e.fail(relaycommon.ModerationDetailTokenPrompt)
				return
			}
		}
		for _, item := range prompt {
			e.add(roleUser, item.(string))
		}
	case []string:
		for _, item := range prompt {
			e.add(roleUser, item)
		}
	default:
		e.fail(relaycommon.ModerationDetailTokenPrompt)
	}
	for _, fim := range []any{req.Prefix, req.Suffix} {
		switch text := fim.(type) {
		case nil:
		case string:
			e.add(roleUser, text)
		default:
			e.fail(relaycommon.ModerationDetailTokenPrompt)
		}
	}
}

// missingCallIDText is the user text the chat → Responses conversion makes
// of a tool message without a call id.
func missingCallIDText(msg *dto.Message) string {
	var output any
	switch {
	case msg.Content == nil:
		output = ""
	case msg.IsStringContent():
		output = msg.StringContent()
	default:
		if b, err := common.Marshal(msg.Content); err == nil {
			output = string(b)
		} else {
			output = fmt.Sprintf("%v", msg.Content)
		}
	}
	return fmt.Sprintf("[tool_output_missing_call_id] %v", output)
}

func (e *extractor) chatContent(role string, content any) {
	var texts []string
	switch v := content.(type) {
	case nil:
		return
	case string:
		e.add(role, v)
		return
	case []any:
		for _, item := range v {
			switch part := item.(type) {
			case map[string]any:
				partType, _ := part["type"].(string)
				switch partType {
				case dto.ContentTypeText:
					if text, ok := part["text"].(string); ok {
						texts = append(texts, text)
					} else {
						e.unchecked()
					}
				case dto.ContentTypeImageURL:
					e.image(chatImageURL(part["image_url"]))
				default:
					e.unchecked()
				}
			case dto.MediaContent:
				texts = e.mediaContent(role, &part, texts)
			default:
				e.unchecked()
			}
		}
	case []dto.MediaContent:
		for i := range v {
			texts = e.mediaContent(role, &v[i], texts)
		}
	default:
		e.unchecked()
	}
	e.add(role, strings.Join(texts, "\n"))
}

func (e *extractor) mediaContent(role string, part *dto.MediaContent, texts []string) []string {
	switch part.Type {
	case dto.ContentTypeText:
		return append(texts, part.Text)
	case dto.ContentTypeImageURL:
		if image := part.GetImageMedia(); image != nil {
			e.image(image.Url)
		}
	default:
		e.unchecked()
	}
	return texts
}

// chatImageURL reads a Chat image_url part as Message.ParseContent does.
func chatImageURL(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case map[string]any:
		url, _ := v["url"].(string)
		return url
	case *dto.MessageImageUrl:
		if v != nil {
			return v.Url
		}
	}
	return ""
}

// responses reads a Responses body by its exact keys, as OpenAI does; a key
// that differs from one of them only in letter case makes the input
// ambiguous, because the gateway's own decoder matches keys ignoring case.
func (e *extractor) responses(input, instructions, prompt []byte) {
	if len(instructions) > 0 {
		var value any
		if err := common.Unmarshal(instructions, &value); err == nil {
			switch v := value.(type) {
			case string:
				e.add(roleSystem, v)
			case []any:
				e.responsesItems(v, "system")
			}
		}
	}
	if len(prompt) > 0 {
		var value any
		if err := common.Unmarshal(prompt, &value); err == nil {
			if object, ok := value.(map[string]any); ok {
				e.promptVariables(object)
			}
		}
	}
	if len(input) > 0 {
		var value any
		if err := common.Unmarshal(input, &value); err == nil {
			switch v := value.(type) {
			case string:
				e.add(roleUser, v)
			case []any:
				e.responsesItems(v, "")
			}
		}
	}
}

func (e *extractor) responsesItems(items []any, defaultRole string) {
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if hasCaseVariant(item, "type", "role", "content") {
			e.fail(relaycommon.ModerationDetailAmbiguousKeys)
		}
		itemType, _ := item["type"].(string)
		role, _ := item["role"].(string)
		content, hasContent := item["content"]
		if itemType != "message" && (itemType != "" || role == "" && !hasContent) {
			continue // tool calls and outputs, reasoning, references
		}
		if role == "" {
			role = defaultRole
		}
		if strings.TrimSpace(role) == "assistant" {
			continue
		}
		class := roleClass(role)
		switch v := content.(type) {
		case string:
			e.add(class, v)
		case []any:
			var texts []string
			for _, rawPart := range v {
				part, ok := rawPart.(map[string]any)
				if !ok {
					e.unchecked()
					continue
				}
				if hasCaseVariant(part, "type", "text", "image_url") {
					e.fail(relaycommon.ModerationDetailAmbiguousKeys)
				}
				partType, _ := part["type"].(string)
				switch partType {
				case "input_text", "text", "output_text":
					if text, ok := part["text"].(string); ok {
						texts = append(texts, text)
					}
				case "input_image":
					url, ok := e.responsesImageURL(part["image_url"])
					if !ok {
						e.unchecked()
						continue
					}
					e.image(url)
				default:
					e.unchecked()
				}
			}
			e.add(class, strings.Join(texts, "\n"))
		}
	}
}

// responsesImageURL reads an input_image image_url: a string or {url}.
func (e *extractor) responsesImageURL(value any) (string, bool) {
	switch v := value.(type) {
	case string:
		return v, v != ""
	case map[string]any:
		if hasCaseVariant(v, "url") {
			e.fail(relaycommon.ModerationDetailAmbiguousKeys)
		}
		url, _ := v["url"].(string)
		return url, url != ""
	}
	return "", false
}

// promptVariables reads the variables of a Responses prompt template, in name
// order: strings and input_text objects are user text, input_image objects
// user images.
func (e *extractor) promptVariables(prompt map[string]any) {
	if hasCaseVariant(prompt, "variables") {
		e.fail(relaycommon.ModerationDetailAmbiguousKeys)
	}
	variables, ok := prompt["variables"].(map[string]any)
	if !ok {
		return
	}
	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		switch v := variables[name].(type) {
		case string:
			e.add(roleUser, v)
		case map[string]any:
			if hasCaseVariant(v, "type", "text", "image_url") {
				e.fail(relaycommon.ModerationDetailAmbiguousKeys)
			}
			switch v["type"] {
			case "input_text":
				text, _ := v["text"].(string)
				e.add(roleUser, text)
			case "input_image":
				if url, ok := e.responsesImageURL(v["image_url"]); ok {
					e.image(url)
				} else {
					e.unchecked()
				}
			default:
				e.unchecked()
			}
		default:
			e.unchecked()
		}
	}
}

// passthroughChat reads a passthrough body the way the Chat Completions
// endpoint it is sent to reads it. A body that does not decode as a chat
// request cannot be checked.
func (e *extractor) passthroughChat(c *gin.Context) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		e.fail(relaycommon.ModerationDetailAmbiguousKeys)
		return
	}
	body, err := storage.Bytes()
	if err != nil {
		e.fail(relaycommon.ModerationDetailAmbiguousKeys)
		return
	}
	var req dto.GeneralOpenAIRequest
	if common.Unmarshal(body, &req) != nil {
		e.fail(relaycommon.ModerationDetailAmbiguousKeys)
		return
	}
	e.chat(&req, false)
}

// claudeSystem reads the top-level system of a Claude request: both
// conversions concatenate the blocks' text, the Responses one keeps only
// text blocks.
func (e *extractor) claudeSystem(req *dto.ClaudeRequest, viaResponses bool) {
	if req.IsStringSystem() {
		e.add(roleSystem, req.GetStringSystem())
		return
	}
	if req.System == nil {
		return
	}
	var system strings.Builder
	for _, block := range req.ParseSystem() {
		if !viaResponses || block.Type == "text" || block.Type == "input_text" || block.Type == "" {
			system.WriteString(block.GetText())
		}
	}
	e.add(roleSystem, system.String())
}

func (e *extractor) claude(req *dto.ClaudeRequest, viaResponses bool) {
	e.claudeSystem(req, viaResponses)
	for i := range req.Messages {
		msg := &req.Messages[i]
		r := strings.TrimSpace(msg.Role)
		if !viaResponses && msg.Role == "assistant" || viaResponses && (r == "assistant" || r == "") {
			continue
		}
		class := roleClass(msg.Role)
		if msg.IsStringContent() {
			e.add(class, msg.GetStringContent())
			continue
		}
		blocks, err := msg.ParseContent()
		if err != nil {
			e.unchecked()
			continue
		}
		var texts []string
		for j := range blocks {
			block := &blocks[j]
			switch block.Type {
			case "text", "input_text":
				texts = append(texts, block.GetText())
			case "image":
				url := relayconvert.ClaudeChatImageURL(block.Source)
				if viaResponses {
					url = relayconvert.ClaudeResponsesSourceURL(block.Source)
				}
				if url == "" {
					e.unchecked()
					continue
				}
				e.image(url)
			case "document":
				if !viaResponses && block.Source != nil && block.Source.Type == "text" {
					texts = append(texts, common.Interface2String(block.Source.Data))
					continue
				}
				e.unchecked()
			case "tool_result":
				// The Chat conversion moves a tool result's images onto the
				// user message; its text stays tool output.
				if !viaResponses && !block.IsStringContent() {
					for _, url := range claudeToolResultImages(block.ParseMediaContent()) {
						e.image(url)
					}
				}
			case "tool_use", "thinking", "redacted_thinking":
			default:
				e.unchecked()
			}
		}
		e.add(class, strings.Join(texts, "\n"))
	}
	e.add(roleUser, req.Prompt)
}

// claudeToolResultImages are the images the Claude → Chat conversion moves
// from a structured tool result onto the following user message: only when
// every block is text or an image it can reference.
func claudeToolResultImages(blocks []dto.ClaudeMediaMessage) []string {
	var urls []string
	for i := range blocks {
		block := &blocks[i]
		switch block.Type {
		case "text", "input_text":
		case "image":
			url := relayconvert.ClaudeChatImageURL(block.Source)
			if url == "" {
				return nil
			}
			urls = append(urls, url)
		default:
			return nil
		}
	}
	return urls
}

func (e *extractor) gemini(req *dto.GeminiChatRequest) {
	if req.SystemInstructions != nil {
		var texts []string
		for _, part := range req.SystemInstructions.Parts {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		e.add(roleSystem, strings.Join(texts, "\n"))
	}
	for _, content := range req.Contents {
		// The conversion maps every role but these two onto user.
		if content.Role == "model" || content.Role == "function" {
			continue
		}
		var texts []string
		for _, part := range content.Parts {
			switch {
			case part.Text != "":
				if !part.Thought {
					texts = append(texts, part.Text)
				}
			case part.InlineData != nil || part.FileData != nil:
				media, reason := relayconvert.GeminiMediaToChatContent(part.InlineData, part.FileData)
				if reason != "" {
					e.unchecked()
					continue
				}
				switch media.Type {
				case dto.ContentTypeText:
					texts = append(texts, media.Text)
				case dto.ContentTypeImageURL:
					if image := media.GetImageMedia(); image != nil {
						e.image(image.Url)
					}
				default:
					e.unchecked()
				}
			}
		}
		e.add(roleUser, strings.Join(texts, "\n"))
	}
	for i := range req.Requests {
		e.gemini(&req.Requests[i])
	}
}

// hasCaseVariant reports whether object holds a key that equals one of names
// ignoring case but not byte for byte.
func hasCaseVariant(object map[string]any, names ...string) bool {
	for key := range object {
		for _, name := range names {
			if key != name && strings.EqualFold(key, name) {
				return true
			}
		}
	}
	return false
}

// passthroughBodyIsAmbiguous reports whether a passthrough body, which
// reaches upstream as the client sent it, holds a case variant of a key the
// extraction reads.
func passthroughBodyIsAmbiguous(c *gin.Context) bool {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return false
	}
	body, err := storage.Bytes()
	if err != nil {
		return false
	}
	var value any
	if common.Unmarshal(body, &value) != nil {
		return false
	}
	top, ok := value.(map[string]any)
	if !ok {
		return false
	}
	if hasCaseVariant(top, "messages", "system", "prompt", "prefix", "suffix", "input", "instructions", "contents", "systemInstruction") {
		return true
	}
	for _, listName := range []string{"messages", "contents"} {
		list, _ := top[listName].([]any)
		for _, rawItem := range list {
			item, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			if hasCaseVariant(item, "role", "content", "parts", "tool_call_id") {
				return true
			}
			for _, partsName := range []string{"content", "parts"} {
				parts, _ := item[partsName].([]any)
				for _, rawPart := range parts {
					part, ok := rawPart.(map[string]any)
					if !ok {
						continue
					}
					if hasCaseVariant(part, "type", "text", "image_url", "source") {
						return true
					}
					if image, ok := part["image_url"].(map[string]any); ok && hasCaseVariant(image, "url") {
						return true
					}
				}
			}
		}
	}
	return false
}
