package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type ProcessingInput struct {
	Job       ProcessingJob
	VideoID   string
	ObjectKey string
}

type Rendition struct {
	Kind          string `json:"kind"`
	ObjectKey     string `json:"object_key"`
	ContentType   string `json:"content_type"`
	Width         int    `json:"width,omitempty"`
	Height        int    `json:"height,omitempty"`
	ConfigVersion int    `json:"config_version"`
}

type ProcessingResult struct {
	Video      Video       `json:"video"`
	Renditions []Rendition `json:"renditions"`
}

type FFmpegProcessor struct {
	store    *S3ObjectStore
	ffprobe  string
	ffmpeg   string
	timeout  time.Duration
	maxBytes int64
}

const renditionScaleFilter = "scale=1280:-2:force_original_aspect_ratio=decrease:force_divisible_by=2"

func NewFFmpegProcessor(store *S3ObjectStore, ffprobe, ffmpeg string, timeout time.Duration, maxBytes int64) *FFmpegProcessor {
	return &FFmpegProcessor{store: store, ffprobe: ffprobe, ffmpeg: ffmpeg, timeout: timeout, maxBytes: maxBytes}
}

func (processor *FFmpegProcessor) Process(ctx context.Context, input ProcessingInput) ([]Rendition, error) {
	ctx, span := otel.Tracer("sea-music/video").Start(ctx, "media.process")
	span.SetAttributes(attribute.String("media.job_id", input.Job.ID), attribute.String("media.asset_id", input.Job.SourceAssetID))
	defer span.End()
	if processor.store == nil || processor.timeout <= 0 || processor.maxBytes <= 0 {
		return nil, errors.New("invalid media processor configuration")
	}
	processCtx, cancel := context.WithTimeout(ctx, processor.timeout)
	defer cancel()
	directory, err := os.MkdirTemp("", "sea-music-media-*")
	if err != nil {
		return nil, fmt.Errorf("create media workspace: %w", err)
	}
	defer os.RemoveAll(directory)
	source := filepath.Join(directory, "source")
	if err := processor.store.DownloadFile(processCtx, input.ObjectKey, source, processor.maxBytes); err != nil {
		if processCtx.Err() != nil {
			return nil, processCtx.Err()
		}
		return nil, err
	}
	metadata, err := processor.probe(processCtx, source)
	if err != nil {
		return nil, err
	}
	playback := filepath.Join(directory, "playback.mp4")
	if err := runMediaCommand(processCtx, processor.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-i", source,
		"-map", "0:v:0", "-map", "0:a?", "-vf", renditionScaleFilter,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-map_metadata", "-1", "-y", playback,
	); err != nil {
		return nil, err
	}
	cover := filepath.Join(directory, "cover.jpg")
	if err := runMediaCommand(processCtx, processor.ffmpeg,
		"-hide_banner", "-loglevel", "error", "-i", source, "-frames:v", "1",
		"-vf", renditionScaleFilter, "-q:v", "3", "-y", cover,
	); err != nil {
		return nil, err
	}
	base := fmt.Sprintf("renditions/%s/v%d", input.Job.SourceAssetID, input.Job.ConfigVersion)
	outputs := []Rendition{
		{Kind: "playback", ObjectKey: base + "/playback.mp4", ContentType: "video/mp4", Width: metadata.Width, Height: metadata.Height, ConfigVersion: input.Job.ConfigVersion},
		{Kind: "cover", ObjectKey: base + "/cover.jpg", ContentType: "image/jpeg", Width: metadata.Width, Height: metadata.Height, ConfigVersion: input.Job.ConfigVersion},
	}
	if err := processor.store.UploadFile(processCtx, outputs[0].ObjectKey, outputs[0].ContentType, playback); err != nil {
		return nil, err
	}
	if err := processor.store.UploadFile(processCtx, outputs[1].ObjectKey, outputs[1].ContentType, cover); err != nil {
		return nil, err
	}
	return outputs, nil
}

type probeMetadata struct {
	Width  int
	Height int
}

func (processor *FFmpegProcessor) probe(ctx context.Context, source string) (probeMetadata, error) {
	ctx, span := otel.Tracer("sea-music/video").Start(ctx, "ffprobe")
	defer span.End()
	command := exec.CommandContext(ctx, processor.ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=width,height:format=duration", "-of", "json", source)
	output, err := command.CombinedOutput()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ffprobe failed")
		if ctx.Err() != nil {
			return probeMetadata{}, ctx.Err()
		}
		return probeMetadata{}, mediaCommandError(processor.ffprobe, output, err)
	}
	var result struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(output, &result); err != nil || len(result.Streams) != 1 || result.Streams[0].Width <= 0 || result.Streams[0].Height <= 0 {
		return probeMetadata{}, errors.New("ffprobe returned invalid video metadata")
	}
	duration, err := strconv.ParseFloat(result.Format.Duration, 64)
	if err != nil || duration <= 0 || duration > 12*60*60 {
		return probeMetadata{}, errors.New("video duration is invalid or exceeds 12 hours")
	}
	return probeMetadata{Width: result.Streams[0].Width, Height: result.Streams[0].Height}, nil
}

func runMediaCommand(ctx context.Context, executable string, arguments ...string) error {
	ctx, span := otel.Tracer("sea-music/video").Start(ctx, "media.command")
	span.SetAttributes(attribute.String("process.executable.name", filepath.Base(executable)))
	defer span.End()
	command := exec.CommandContext(ctx, executable, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "media command failed")
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return mediaCommandError(executable, output, err)
	}
	return nil
}

func mediaCommandError(executable string, output []byte, err error) error {
	message := strings.TrimSpace(string(output))
	if len(message) > 1000 {
		message = message[:1000]
	}
	return fmt.Errorf("%s failed: %s: %w", filepath.Base(executable), message, err)
}

type ProcessingService struct {
	repository    *PostgresRepository
	processor     *FFmpegProcessor
	workerID      string
	leaseDuration time.Duration
}

func NewProcessingService(repository *PostgresRepository, processor *FFmpegProcessor, workerID string, leaseDuration time.Duration) *ProcessingService {
	return &ProcessingService{repository: repository, processor: processor, workerID: workerID, leaseDuration: leaseDuration}
}

func (service *ProcessingService) RunOnce(ctx context.Context) (ProcessingResult, error) {
	job, err := service.repository.ClaimProcessingJob(ctx, service.workerID, service.leaseDuration)
	if err != nil {
		return ProcessingResult{}, err
	}
	input, err := service.repository.StartProcessingJob(ctx, job.ID, service.workerID)
	if err != nil {
		return ProcessingResult{}, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	renewed := make(chan error, 1)
	go service.renewLease(processCtx, cancel, job.ID, renewed)
	renditions, err := service.processor.Process(processCtx, input)
	cancel()
	renewErr := <-renewed
	if renewErr != nil {
		return ProcessingResult{}, renewErr
	}
	if err != nil {
		failErr := service.repository.FailProcessingJob(ctx, job.ID, service.workerID, err.Error(), time.Second)
		if failErr != nil {
			return ProcessingResult{}, errors.Join(err, failErr)
		}
		return ProcessingResult{}, err
	}
	return service.repository.CompleteProcessingJob(ctx, job.ID, service.workerID, renditions)
}

func (service *ProcessingService) renewLease(ctx context.Context, cancel context.CancelFunc, jobID string, result chan<- error) {
	interval := service.leaseDuration / 3
	if interval <= 0 {
		result <- errors.New("invalid lease heartbeat interval")
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result <- nil
			return
		case <-ticker.C:
			if _, err := service.repository.RenewProcessingLease(ctx, jobID, service.workerID, service.leaseDuration); err != nil {
				cancel()
				result <- err
				return
			}
		}
	}
}
