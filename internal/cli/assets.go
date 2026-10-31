package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/orshih6/pikactl/internal/api"
	"github.com/orshih6/pikactl/internal/catalog"
	"github.com/orshih6/pikactl/internal/history"
	"github.com/orshih6/pikactl/internal/media"
)

func newUploadsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uploads",
		Short: "Host local files for use as inputs",
		Long: "Host local files for use as inputs. `pikactl run` does this by itself for a local\n" +
			"path, so this is for using one file in several requests.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "put <file>...",
		Short: "Upload files and print their hosted URLs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			var uploads []*media.Upload
			for _, path := range args {
				upload, err := a.media.Upload(cmd.Context(), expandHome(path))
				if err != nil {
					return err
				}
				uploads = append(uploads, upload)
			}
			return emit(a.out, uploads, func() error {
				for _, upload := range uploads {
					fmt.Fprintln(a.out, upload.URL)
				}
				return nil
			})
		},
	}, &cobra.Command{
		Use:   "delete <url>",
		Short: "Erase one uploaded file by its hosted URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			if err := confirm(cmd, "Erase "+args[0]+"?"); err != nil {
				return err
			}
			if err := a.media.DeleteUpload(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Erased.")
			return nil
		},
	})
	return cmd
}

func newVoicesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "voices", Short: "List, clone and delete voices"}
	cmd.AddCommand(
		endpoint{use: "list", short: "List the voices available to the organisation", method: http.MethodGet, path: "/v1/voices"}.command(),
		endpoint{use: "get", short: "Show one voice", method: http.MethodGet, path: "/v1/voices/{voice_id}"}.command(),
		endpoint{use: "delete", short: "Delete a cloned voice", method: http.MethodDelete, path: "/v1/voices/{voice_id}", ask: "Delete this voice?"}.command(),
		newVoicesCreateCommand(),
	)
	return cmd
}

func newVoicesCreateCommand() *cobra.Command {
	var name string
	var samples []string
	var consent bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Clone a voice from recordings of one speaker",
		Long: "Clone a voice from clean recordings of one speaker; one to two minutes in total\n" +
			"works best. The API requires you to attest that you have the speaker's\n" +
			"permission, so --consent is yours to give and pikactl never assumes it.",
		Example: `  pikactl voices create --name narrator --sample ./take1.mp3 --sample ./take2.mp3 --consent`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !consent {
				return errors.New("--consent is required: it attests that you have the speaker's permission to clone their voice")
			}
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			urls := make([]string, 0, len(samples))
			for _, sample := range samples {
				if strings.HasPrefix(sample, "http://") || strings.HasPrefix(sample, "https://") {
					urls = append(urls, sample)
					continue
				}
				upload, err := a.media.Upload(cmd.Context(), expandHome(sample))
				if err != nil {
					return err
				}
				urls = append(urls, upload.URL)
			}
			body, err := json.Marshal(map[string]any{"name": name, "sample_urls": urls, "consent_attested": true})
			if err != nil {
				return err
			}
			if err := confirm(cmd, "Clone a voice named "+name+" from "+fmt.Sprint(len(urls))+" recording(s)?"); err != nil {
				return err
			}
			ref := a.hist.NewRef()
			if err := a.hist.Append(history.Event{Type: history.EventRequest, Ref: ref, Kind: history.KindAPI, Method: http.MethodPost, Path: "/v1/voices", Body: body}); err != nil {
				return fmt.Errorf("could not write to the history, so nothing was sent: %w", err)
			}
			resp, err := a.api.Do(cmd.Context(), api.Request{Method: http.MethodPost, Path: "/v1/voices", Body: body})
			a.recordAPIResult(ref, resp, err)
			if err != nil {
				return err
			}
			return printRawJSON(a.out, resp.Body)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the voice's name")
	cmd.Flags().StringArrayVar(&samples, "sample", nil, "a recording, as a local file or URL (repeatable)")
	cmd.Flags().BoolVar(&consent, "consent", false, "attest that you have the speaker's permission")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("sample")
	return cmd
}

// A language model is reached over one of three protocols, which only its
// documentation names. They are told apart by the path it gives.
const (
	protocolAnthropic = "anthropic"
	protocolOpenAI    = "openai"
	protocolGenAI     = "genai"
)

func protocolOf(path string) string {
	switch {
	case strings.HasPrefix(path, "/anthropic/"):
		return protocolAnthropic
	case strings.HasPrefix(path, "/genai/"):
		return protocolGenAI
	case strings.HasSuffix(path, "/chat/completions"):
		return protocolOpenAI
	}
	return ""
}

func chatBody(protocol, model, system, prompt string, maxTokens int) map[string]any {
	switch protocol {
	case protocolAnthropic:
		body := map[string]any{
			"model":      model,
			"max_tokens": maxTokens,
			"messages":   []any{map[string]any{"role": "user", "content": prompt}},
		}
		if body["max_tokens"] == 0 {
			body["max_tokens"] = 1024 // required by this protocol
		}
		if system != "" {
			body["system"] = system
		}
		return body
	case protocolGenAI:
		body := map[string]any{
			"contents": []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": prompt}}}},
		}
		if system != "" {
			body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": system}}}
		}
		if maxTokens > 0 {
			body["generationConfig"] = map[string]any{"maxOutputTokens": maxTokens}
		}
		return body
	}
	messages := []any{}
	if system != "" {
		messages = append(messages, map[string]any{"role": "system", "content": system})
	}
	messages = append(messages, map[string]any{"role": "user", "content": prompt})
	body := map[string]any{"model": model, "messages": messages}
	if maxTokens > 0 {
		body["max_tokens"] = maxTokens
	}
	return body
}

// chatText pulls the reply out of an answer. Empty means there is no plain text
// to show, and the caller prints the whole answer instead.
func chatText(protocol string, raw []byte) string {
	var answer struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(raw, &answer) != nil {
		return ""
	}
	var text strings.Builder
	switch protocol {
	case protocolAnthropic:
		for _, block := range answer.Content {
			text.WriteString(block.Text)
		}
	case protocolOpenAI:
		if len(answer.Choices) > 0 {
			text.WriteString(answer.Choices[0].Message.Content)
		}
	case protocolGenAI:
		if len(answer.Candidates) > 0 {
			for _, part := range answer.Candidates[0].Content.Parts {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

func newChatCommand() *cobra.Command {
	var system, source, note string
	var maxTokens int
	var sets []string
	cmd := &cobra.Command{
		Use:   "chat <model> [prompt...]",
		Short: "Ask a language model, over whichever protocol it uses",
		Long: "Ask a language model. The protocol (Anthropic, OpenAI or Google) is read from the\n" +
			"model's documentation. The prompt comes from the arguments or stdin. It bills by\n" +
			"the tokens used, which has no quote and so no question first; --max-tokens bounds\n" +
			"it. For streaming, send the request with `pikactl api`.",
		Example: `  pikactl chat anthropic/claude-opus-5.5 "Write a five-word title for a video about tides"
  git diff | pikactl chat openai/gpt-5.6-sol --system "Review this diff" --set temperature=0.2`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			id := args[0]
			if op, err := a.catalog.Get(cmd.Context(), id); err != nil {
				return err
			} else if op.Call != nil {
				return fmt.Errorf("%s is a job: use `pikactl run %s`", id, id)
			}
			ep, err := a.catalog.Endpoint(cmd.Context(), id)
			if err != nil {
				return err
			}
			protocol := protocolOf(ep.Path)
			if protocol == "" {
				return fmt.Errorf("%s is called at %s, which is not a chat protocol: use `pikactl api`", id, ep.Path)
			}
			body, err := startBody(cmd, source)
			if err != nil {
				return err
			}
			if source == "" {
				prompt := strings.Join(args[1:], " ")
				if prompt == "" {
					data, err := readData(cmd, "-")
					if err != nil {
						return err
					}
					prompt = strings.TrimSpace(string(data))
				}
				if prompt == "" {
					return errors.New("no prompt: give one as an argument or on stdin")
				}
				body = chatBody(protocol, id, system, prompt, maxTokens)
			}
			if err := applySets(&catalog.Schema{}, body, sets); err != nil {
				return err
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			answer, err := a.callSync(cmd, history.KindChat, id, ep, raw, note)
			if err != nil {
				return err
			}
			text := chatText(protocol, answer)
			if flagJSON || text == "" {
				return printRawJSON(a.out, answer)
			}
			fmt.Fprintln(a.out, text)
			return nil
		},
	}
	cmd.Flags().StringVar(&system, "system", "", "system prompt")
	cmd.Flags().IntVar(&maxTokens, "max-tokens", 0, "most tokens to generate (default 1024 where the protocol requires a value)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "set any other request field, key=value (repeatable)")
	cmd.Flags().StringVar(&source, "body", "", "send this JSON body instead: literal JSON, @file.json, or - for stdin")
	cmd.Flags().StringVar(&note, "note", "", "a label to keep with this request in the history")
	return cmd
}

func newDecideCommand() *cobra.Command {
	var state, questions, source, note string
	cmd := &cobra.Command{
		Use:   "decide <model>",
		Short: "Get typed answers (noul, choice, score) about a state",
		Long: "Get one typed answer per question about a state you send. See\n" +
			"`pikactl models docs <model>` for the question format.",
		Example: `  pikactl decide typesafe/jev-1.13 --state @ticket.json --questions @questions.json`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := newApp(cmd)
			if err != nil {
				return err
			}
			if err := a.cfg.RequireKey(); err != nil {
				return err
			}
			id := args[0]
			if _, err := a.catalog.Get(cmd.Context(), id); err != nil {
				return err
			}
			ep, err := a.catalog.Endpoint(cmd.Context(), id)
			if err != nil {
				return err
			}
			body, err := startBody(cmd, source)
			if err != nil {
				return err
			}
			if source == "" {
				if state == "" || questions == "" {
					return errors.New("--state and --questions are required unless --body is given")
				}
				if err := applySets(&catalog.Schema{}, body, []string{"state=" + state, "questions=" + questions}); err != nil {
					return err
				}
			}
			if _, ok := body["model"]; !ok {
				body["model"] = id
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			answer, err := a.callSync(cmd, history.KindDecision, id, ep, raw, note)
			if err != nil {
				return err
			}
			return printRawJSON(a.out, answer)
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "the state, as JSON or @file.json")
	cmd.Flags().StringVar(&questions, "questions", "", "the questions, as JSON or @file.json")
	cmd.Flags().StringVar(&source, "body", "", "send this JSON body instead: literal JSON, @file.json, or - for stdin")
	cmd.Flags().StringVar(&note, "note", "", "a label to keep with this request in the history")
	return cmd
}

// syncTimeout allows for a long language model answer.
const syncTimeout = 10 * time.Minute

// callSync makes one synchronous, billed call and records it. It is not
// retried: a call whose answer was lost may still have been charged.
func (a *app) callSync(cmd *cobra.Command, kind, id string, ep *catalog.Endpoint, raw []byte, note string) ([]byte, error) {
	ref := a.hist.NewRef()
	if err := a.hist.Append(history.Event{
		Type: history.EventRequest, Ref: ref, Kind: kind, APIID: id,
		Method: http.MethodPost, Path: ep.Path, Body: raw, Note: note,
	}); err != nil {
		return nil, fmt.Errorf("could not write to the history, so nothing was sent: %w", err)
	}
	header := http.Header{}
	for name, value := range ep.Headers {
		header.Set(name, value)
	}
	resp, err := a.api.Do(cmd.Context(), api.Request{
		Method: http.MethodPost, Path: ep.Path, Body: raw, Header: header, Timeout: syncTimeout,
	})
	a.recordAPIResult(ref, resp, err)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
