package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {

	r.Body = http.MaxBytesReader(w, r.Body, 1 << 30)


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

	fmt.Println("uploading video", videoID, "by user", userID)


	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Video not found", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Not video owner", nil)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't read file", err)
		return
	}
	defer file.Close()

	fileType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}
	if fileType != "video/mp4" {
		respondWithError(w, http.StatusUnsupportedMediaType, "Media type not mp4", nil)
		return
	}


	fileOnDisk, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusForbidden, "Unable to create file on disk", err)
		return
	}
	defer os.Remove("tubely-upload.mp4")
	defer fileOnDisk.Close()

	_, err = io.Copy(fileOnDisk, file)
	fileOnDisk.Seek(0, io.SeekStart)

	pathKey := make([]byte, 32)
	rand.Read(pathKey)
	videoKey := hex.EncodeToString(pathKey)

	s3Struct := s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key:	&videoKey,
		Body: fileOnDisk,
		ContentType: &fileType,
	}

	cfg.s3Client.PutObject(r.Context(), &s3Struct)

	// Format:
	// https://<bucket-name>.s3.<region>.amazonaws.com/<key>
	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, videoKey)
	video.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error updating video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}

func getVideoAspectRatio(filePath string) (string, error) {
	type output struct {
		Streams []struct {
			Width	int `json:"width"`
			Height	int `json:"height"`
		} `json:"streams"`
	}

	// ffprobe -v error -print_format json -show_streams PATH_TO_VIDEO
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	var outputData output
    if err := json.Unmarshal(out.Bytes(), &outputData); err != nil {
        return "", err
    }

    height := outputData.Streams[0].Height
    width  := outputData.Streams[0].Width

    // strings to return "16:9", "9:16", or "other"
    if width * 9 == height * 16 {
    	// is horizontal
    	return "16:9", nil
    } else if width * 16 == height * 9 {
    	// is vertical
    	return "9:16", nil
    } else {
    	// is other
    	return "other", nil
    }
}
