package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

type FFProbeOutput struct {
	Streams []struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"streams"`
}

func GetVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var stdBuffer bytes.Buffer
	cmd.Stdout = &stdBuffer

	err := cmd.Run()
	if err != nil {
		return "", err
	}

	var probeData FFProbeOutput
	if err = json.Unmarshal(stdBuffer.Bytes(), &probeData); err != nil {
		return "", err
	}

	if len(probeData.Streams) == 0 || probeData.Streams[0].Width == 0 || probeData.Streams[0].Height == 0 {
		return "", errors.New("could not detect valid video streams data")
	}
	width := probeData.Streams[0].Width
	height := probeData.Streams[0].Height

	// 5. Perform the aspect ratio math comparison
	// We check standard ratios using cross-multiplication to handle float rounding safety
	const tolerance = 0.02
	actualRatio := float64(width) / float64(height)

	if math.Abs(actualRatio-16.0/9.0) < tolerance {
		return "16:9", nil
	} else if math.Abs(actualRatio-9.0/16.0) < tolerance {
		return "9:16", nil
	}

	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"
	cmd := exec.Command("ffmpeg",
		"-y",
		"-i", filePath,
		"-c", "copy",
		"-movflags", "faststart",
		"-f", "mp4",
		outputPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("ffmpeg failed: %w (output: %s)", err, string(output))
	}
	return outputPath, nil
}

func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	ctx := context.Background()
	duration := time.Duration(expireTime)
	presignClient := s3.NewPresignClient(s3Client)
	presignedReq, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &bucket,
		Key:    &key,
	}, func(opts *s3.PresignOptions) {
		opts.Expires = duration
	})
	if err != nil {
		fmt.Printf("there was an error creating the options %s", err)
		return "", err
	}

	return presignedReq.URL, nil
}

func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil {
		return video, nil
	}
	parts := strings.Split(*video.VideoURL, ",")
	if len(parts) < 2 {
		return database.Video{}, fmt.Errorf("invalid video URL format on database record %s: expected 'bucket,key'", video.ID)
	}
	bucket := strings.TrimSpace(parts[0])
	key := strings.TrimSpace(parts[1])
	if bucket == "" || key == "" {
		return database.Video{}, errors.New("malformed database video URL: empty bucket or key parsed")
	}

	expiration := 15 * time.Minute

	presignedURL, err := generatePresignedURL(cfg.S3Client, bucket, key, expiration)
	if err != nil {
		return database.Video{}, fmt.Errorf("failed to generate signed URL for key %s: %w", key, err)
	}
	video.VideoURL = &presignedURL
	return video, nil
}

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

	ratio, err := GetVideoAspectRatio(tempfile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, " error reading the ratios", err)
		return
	}

	var prefix string
	switch ratio {
	case "16:9":
		prefix = "landscape/"
	case "9:16":
		prefix = "portrait/"
	default:
		prefix = "other/"
	}

	processedFilePath, err := processVideoForFastStart(tempfile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "an error getting the file path", err)
		return
	}
	defer os.Remove(processedFilePath)

	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error opening processed video", err)
		return
	}
	defer processedFile.Close()

	randombytes := make([]byte, 32)
	_, err = rand.Read(randombytes)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "error doing conversion", err)
		return
	}

	filekey := fmt.Sprintf("%s%s.mp4", prefix, hex.EncodeToString(randombytes))

	_, err = cfg.S3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(filekey),
		Body:        processedFile,
		ContentType: aws.String("video/mp4"),
	})

	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "failed uploading video to s3", err)
		return
	}

	videoURL := fmt.Sprintf("%s,%s", cfg.s3Bucket, filekey)
	video.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "there was an error making the update", err)
		return
	}

	signedVideo, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "could not create a presigned url", err)
		return
	}

	respondWithJSON(w, http.StatusOK, signedVideo)

}
