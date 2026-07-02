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

	videoForm := "other"
	aspectRatio, err := getVideoAspectRatio(fileOnDisk.Name())
	if aspectRatio == "16:9" {
		// landscape
		videoForm = "landscape"
	} else if aspectRatio == "9:16" {
		// portrait
		videoForm = "portrait"
	}

	processedFileName, err := processVideoForFastStart(fileOnDisk.Name())
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error converting video", err)
		return
	}
	processedFile, err := os.Open(processedFileName)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Couldn't open file", err)
		return
	}
	defer os.Remove(processedFileName)
	defer processedFile.Close()

	pathKey := make([]byte, 32)
	rand.Read(pathKey)
	videoKey := videoForm + "/" + hex.EncodeToString(pathKey)

	s3Struct := s3.PutObjectInput{
		Bucket: &cfg.s3Bucket,
		Key:	&videoKey,
		Body: processedFile,
		ContentType: &fileType,
	}

	cfg.s3Client.PutObject(r.Context(), &s3Struct)

	// Format:
	// https://<bucket-name>.s3.<region>.amazonaws.com/<key>
	// <distribution-domain>.cloudfront.net
	// videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, videoKey)
	videoURL := fmt.Sprintf("https://%s.cloudfront.net/%s", cfg.s3CfDistribution, videoKey)
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
    ratio := width * 9 / height
    if ratio == 16 || ratio == 15 {
    	// is horizontal
    	return "16:9", nil
    } 
    ratio = height * 9 / width
    if ratio == 16 || ratio == 15 {
    	// is vertical
    	return "9:16", nil
    }
	// is other
	return "other", nil
}

func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"

	// The command is ffmpeg and the arguments are:
	// -i, the input file path, 
	// -c, copy, 
	// -movflags, faststart, 
	// -f, mp4 
	// and the output file path.
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy","-movflags", "faststart", "-f", "mp4", outputPath)
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	return outputPath, nil
}
