package api

import (
	"net/http"

	"github.com/IliaSA101/sosninaalbums/api/albums"
)

// Обработчики альбомов. Вся логика, права и пределы — в internal/album; здесь только разбор запроса и
// перевод ошибок в ответы контракта.

func (h *Handler) create(c *call) {
	release, ok := h.acquireJSON(c)
	if !ok {
		return
	}
	defer release()
	req, ok := c.readCreate(h.limits.Pages)
	if !ok {
		return
	}
	out, err := h.svc.Create(c.owner, req)
	if err != nil {
		c.svcError(err)
		return
	}
	c.json(http.StatusCreated, out)
}

func (h *Handler) update(c *call) {
	release, ok := h.acquireJSON(c)
	if !ok {
		return
	}
	defer release()
	req, ok := c.readUpdate(h.limits.Pages)
	if !ok {
		return
	}
	missing, err := h.svc.Update(c.owner, c.id, req.Manifest)
	if err != nil {
		c.svcError(err)
		return
	}
	c.json(http.StatusOK, albums.Updated{Missing: missing})
}

func (h *Handler) patch(c *call) {
	release, ok := h.acquireJSON(c)
	if !ok {
		return
	}
	defer release()
	var p albums.Patch
	if !c.readJSON(&p) {
		return
	}
	out, err := h.svc.Patch(c.owner, c.id, &p)
	if err != nil {
		c.svcError(err)
		return
	}
	c.json(http.StatusOK, out)
}

func (h *Handler) remove(c *call) {
	if err := h.svc.Delete(c.owner, c.id); err != nil {
		c.svcError(err)
		return
	}
	c.w.Header().Set("Cache-Control", "no-store")
	c.w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) publish(c *call) {
	out, err := h.svc.Publish(c.owner, c.id)
	if err != nil {
		c.svcError(err)
		return
	}
	c.json(http.StatusOK, out)
}
