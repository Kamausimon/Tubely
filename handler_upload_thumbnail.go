package main

import (
	"fmt"
	"io"
	"net/http"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// TODO: implement the upload here
	const maxMemory = 10 << 20

	err = r.ParseMultipartForm(maxMemory)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not parse data", err)
	}

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "error getting details from the thumbnail", err)
	}
	defer file.Close()
	mediaType := header.Header.Get("Content-Type")

	imageData, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "error getting image data from the body", err)
	}

	videoMetadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "unauthorized", err)
	}

	tn := thumbnail{
		data:      imageData,
		mediaType: mediaType,
	}

	videoThumbnails[videoID] = tn

	thumbnailURL := fmt.Sprintf("http.localhost:%s/api/thumbnails/%s", cfg.port, videoID)

	videoMetadata.ThumbnailURL = &thumbnailURL

	err = cfg.db.UpdateVideo(videoMetadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "there was an error making the update", err)
	}

	respondWithJSON(w, http.StatusOK, struct{}{})
}
