package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremyVun/MediaServer/internal/store"
)

func TestCollectionsAPI(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	first, err := st.CreateItem(ctx, store.NewItem{Title: "First"})
	if err != nil {
		t.Fatalf("first item: %v", err)
	}
	second, err := st.CreateItem(ctx, store.NewItem{Title: "Second"})
	if err != nil {
		t.Fatalf("second item: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/collections", strings.NewReader(`{"name":"Favourites"}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var collection collectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &collection); err != nil {
		t.Fatalf("decode collection: %v", err)
	}
	if collection.ID == 0 || collection.Name != "Favourites" {
		t.Fatalf("collection = %+v", collection)
	}

	addCollectionItem(t, srv, collection.ID, first.ID)
	addCollectionItem(t, srv, collection.ID, second.ID)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PUT", "/api/collections/"+strconv.FormatInt(collection.ID, 10)+"/order",
		strings.NewReader(`{"item_ids":[`+strconv.FormatInt(second.ID, 10)+`,`+strconv.FormatInt(first.ID, 10)+`]}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reorder status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/items?collection_id="+strconv.FormatInt(collection.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list itemListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Total != 2 || list.Items[0].ID != second.ID || list.Items[1].ID != first.ID {
		t.Fatalf("filtered list = %+v", list)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/collections", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var collections []collectionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &collections); err != nil {
		t.Fatalf("decode collections: %v", err)
	}
	if len(collections) != 1 || collections[0].ItemCount != 2 || len(collections[0].ThumbURLs) != 2 {
		t.Fatalf("collections = %+v", collections)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("PATCH", "/api/collections/"+strconv.FormatInt(collection.ID, 10), strings.NewReader(`{"name":"Watchlist"}`))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/collections/"+strconv.FormatInt(collection.ID, 10)+"/items/"+strconv.FormatInt(first.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove item status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("DELETE", "/api/collections/"+strconv.FormatInt(collection.ID, 10), nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete collection status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func addCollectionItem(t *testing.T, srv *Server, collectionID, itemID int64) {
	t.Helper()
	rec := httptest.NewRecorder()
	body := `{"item_id":` + strconv.FormatInt(itemID, 10) + `}`
	req := httptest.NewRequest("POST", "/api/collections/"+strconv.FormatInt(collectionID, 10)+"/items", strings.NewReader(body))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("add collection item status=%d body=%s", rec.Code, rec.Body.String())
	}
}
