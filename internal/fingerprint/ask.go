package fingerprint

import (
	"context"
	"errors"
	"strings"

	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

// errReplyExceeds stops the request of a question whose reply already holds more numbers than asked.
var errReplyExceeds = errors.New("fingerprint: the reply holds more numbers than asked")

// Ask sends one question of a round and reads its reply. request is the request of the round without a prompt.
// onEvent, when set, sees every stream event before the reply is read, and an error it returns stops the request.
//
// The request is stopped once the reply holds more numbers than asked, which is not a failure. The answer is what
// arrived, including the part of a reply that came before its request failed. The error is the failure of the
// request.
func Ask(ctx context.Context, client *upstream.Client, request upstream.Request, challenge domain.FingerprintChallenge, onEvent func(upstream.Event) error) (upstream.Result, Answer, error) {
	var reply strings.Builder
	request.Prompt = challenge.Prompt
	response, err := client.Generate(ctx, request, func(event upstream.Event) error {
		if onEvent != nil {
			if err := onEvent(event); err != nil {
				return err
			}
		}
		if event.Type != "text" {
			return nil
		}
		reply.WriteString(event.Text)
		if Exceeds(reply.String(), challenge.ExpectedCount) {
			return errReplyExceeds
		}
		return nil
	})
	if errors.Is(err, errReplyExceeds) {
		err = nil
	}
	return response, NewAnswer(response.Text, challenge.ExpectedCount), err
}
