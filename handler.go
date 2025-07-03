package yasctx

import (
	"context"
	"log/slog"
	"slices"
	"time"
)

// attrExtractor is a function that retrieves or creates slog.Attr's based
// information/values found in the context.Context and the slog.Record's basic
// attributes.
type attrExtractor func(ctx context.Context, recordT time.Time, recordLvl slog.Level, recordMsg string) []slog.Attr

// Handler is a slog.Handler middleware that will Prepend and
// Append attributes to log lines. The attributes are extracted out of the log
// record's context by the provided AttrExtractor methods.
// It passes the final record and attributes off to the next handler when finished.
type Handler struct {
	next       slog.Handler
	goa        *groupOrAttrs
	prependers []attrExtractor
}

var _ slog.Handler = &Handler{} // Assert conformance with interface

// NewMiddleware creates a yasctx.Handler slog.Handler middleware
// that conforms to [github.com/samber/slog-multi.Middleware] interface.
// It can be used with slogmulti methods such as Pipe to easily setup a pipeline of slog handlers:
//
//	slog.SetDefault(slog.New(slogmulti.
//		Pipe(yasctx.NewMiddleware()).
//		Pipe(slogdedup.NewOverwriteMiddleware(&slogdedup.OverwriteHandlerOptions{})).
//		Handler(slog.NewJSONHandler(os.Stdout)),
//	))
func NewMiddleware() func(slog.Handler) slog.Handler {
	return func(next slog.Handler) slog.Handler {
		return NewHandler(
			next,
		)
	}
}

// NewHandler creates a Handler slog.Handler middleware that will Prepend and
// Append attributes to log lines. The attributes are extracted out of the log
// record's context by the provided AttrExtractor methods.
// It passes the final record and attributes off to the next handler when finished.
// If opts is nil, the default options are used.
func NewHandler(next slog.Handler) *Handler {

	prependers := []attrExtractor{
		extractPropagatedAttrs,
		extractAdded,
	}

	return &Handler{
		next:       next,
		prependers: prependers,
	}
}

// Enabled reports whether the next handler handles records at the given level.
// The handler ignores records whose level is lower.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle de-duplicates all attributes and groups, then passes the new set of attributes to the next handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {

	// Initialize a mapping from extractAddedToGroup() with added bool to track which groups were used.
	// This will allow us to prepend any unused groups to the final attributes.
	addedToGroup := map[string]*struct {
		attrs []slog.Attr
		used  bool
	}{}
	for k, v := range extractAddedToGroup(ctx, r.Time, r.Level, r.Message) {
		addedToGroup[k] = &struct {
			attrs []slog.Attr
			used  bool
		}{
			attrs: v,
			used:  false,
		}
	}

	// Collect all attributes from the record (which is the most recent attribute set).
	// These attributes are ordered from oldest to newest, and our collection will be too.
	finalAttrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		finalAttrs = append(finalAttrs, a)
		return true
	})

	// Iterate through the goa (group Or Attributes) linked list, which is ordered from newest to oldest
	for g := h.goa; g != nil; g = g.next {
		if g.group != "" {
			if ctxGroupAttrs, ok := addedToGroup[g.group]; ok {
				// If we have attributes for this group, we will use them.
				if !ctxGroupAttrs.used {
					// Mark this group as used, so we don't use it again.
					ctxGroupAttrs.used = true
					finalAttrs = append(slices.Clip(ctxGroupAttrs.attrs), finalAttrs...)
				}
			}
			// If a group, put all the previous attributes (the newest ones) in it
			finalAttrs = []slog.Attr{{
				Key:   g.group,
				Value: slog.GroupValue(finalAttrs...),
			}}
		} else {
			// Prepend to the front of finalAttrs, thereby making finalAttrs ordered from oldest to newest
			finalAttrs = append(slices.Clip(g.attrs), finalAttrs...)
		}
	}

	// Add in any unsued group attributes that were not used to the start (root)
	for _, ctxGroupAttrs := range addedToGroup {
		if !ctxGroupAttrs.used {
			finalAttrs = append(slices.Clip(ctxGroupAttrs.attrs), finalAttrs...)
		}
	}

	// Add our 'prepended' context attributes to the start.
	// Go in reverse order, since each is prepending to the front.
	for i := len(h.prependers) - 1; i >= 0; i-- {
		finalAttrs = append(slices.Clip(h.prependers[i](ctx, r.Time, r.Level, r.Message)), finalAttrs...)
	}

	// Add all attributes to new record (because old record has all the old attributes as private members)
	newR := &slog.Record{
		Time:    r.Time,
		Level:   r.Level,
		Message: r.Message,
		PC:      r.PC,
	}

	// Add attributes back in
	newR.AddAttrs(finalAttrs...)
	return h.next.Handle(ctx, *newR)
}

// WithGroup returns a new AppendHandler that still has h's attributes,
// but any future attributes added will be namespaced.
func (h *Handler) WithGroup(name string) slog.Handler {
	h2 := *h
	h2.goa = h2.goa.WithGroup(name)
	return &h2
}

// WithAttrs returns a new AppendHandler whose attributes consists of h's attributes followed by attrs.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h2 := *h
	h2.goa = h2.goa.WithAttrs(attrs)
	return &h2
}
