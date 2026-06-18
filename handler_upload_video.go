package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
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

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Couldn't get video", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You are not authorzed to view this resource", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "error parsing the video", err)
		return
	}
	defer file.Close()
	videoType := header.Header.Get("Content-Type")
	videoTypeStr, _, err := mime.ParseMediaType(videoType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "error parsing the media type", err)
		return
	}
	if videoTypeStr != "video/mp4" {
		respondWithError(w, http.StatusUnsupportedMediaType, "upload a supported media type", err)
		return
	}

	tempfile, err := os.CreateTemp("", "tubely.upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "error creating temp file", err)
		return
	}

	defer os.Remove(tempfile.Name())
	defer tempfile.Close()

	_, err = io.Copy(tempfile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error copy the file materials", err)
		return
	}

	_, err = tempfile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error resolving to start of file", err)
		return
	}

	randombytes := make([]byte, 32)
	_, err = rand.Read(randombytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error doing conversion", err)
		return
	}

	filekey := fmt.Sprintf("%s.mp4", hex.EncodeToString(randombytes))

	_, err = cfg.S3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(filekey),
		Body:        tempfile,
		ContentType: aws.String("video/mp4"),
	})

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed uploading video to s3", err)
		return
	}

	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, filekey)
	video.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "there was an error making the update", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video.VideoURL)

}
