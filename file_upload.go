package notionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"
)

// FileUploadID is the unique identifier for a Notion file upload.
type FileUploadID string

func (fuid FileUploadID) String() string {
	return string(fuid)
}

// FileUploadMode defines how the file is being sent.
type FileUploadMode string

const (
	// FileUploadModeSinglePart is for files up to 20MB, sent in one go.
	FileUploadModeSinglePart FileUploadMode = "single_part"
	// FileUploadModeMultiPart is for files larger than 20MB, sent in multiple parts.
	FileUploadModeMultiPart FileUploadMode = "multi_part"
	// FileUploadModeExternalURL is for files hosted publicly elsewhere.
	FileUploadModeExternalURL FileUploadMode = "external_url"
)

// FileUploadStatus defines the current state of a file upload.
type FileUploadStatus string

const (
	// FileUploadStatusPending means awaiting upload or completion of an upload.
	FileUploadStatusPending FileUploadStatus = "pending"
	// FileUploadStatusUploaded means file contents have been sent.
	FileUploadStatusUploaded FileUploadStatus = "uploaded"
	// FileUploadStatusExpired means the file upload can no longer be used.
	FileUploadStatusExpired FileUploadStatus = "expired"
	// FileUploadStatusFailed means the import was unsuccessful (for external_url mode).
	FileUploadStatusFailed FileUploadStatus = "failed"
)

// ObjectType typically represents the type of a Notion object.
// This might already be defined in your project. If not, you can use this.
// type ObjectType string

// Assuming ObjectTypeFileUpload would be "file_upload"
// const ObjectTypeFileUpload ObjectType = "file_upload"

// FileUploadService provides an interface for file upload operations.
type FileUploadService interface {
	// Create initiates the process of uploading a file.
	Create(ctx context.Context, request *FileUploadCreateRequest) (*FileUpload, error)
	// Send transmits file contents to Notion for a specific file upload.
	// file is an io.Reader for the file content.
	// fileName is the name of the file being uploaded (e.g., "image.png").
	// partNumber is required and indicates the current part number when mode is multi_part. Should be >= 1.
	Send(ctx context.Context, id FileUploadID, file io.Reader, fileName string, partNumber *int) error
}

// FileUploadClient implements FileUploadService.
type FileUploadClient struct {
	apiClient *Client // Assumes Client has a method like `request` that can handle different body types and content types.
}

// FileUploadCreateRequest represents the request body for FileUploadClient.Create.
// See https://developers.notion.com/reference/create-file-upload
type FileUploadCreateRequest struct {
	// How the file is being sent. Defaults to single_part.
	Mode FileUploadMode `json:"mode,omitempty"`
	// Name of the file to be created. Required when mode is multi_part or external_url.
	// Must include an extension, or have one inferred from the content_type.
	Filename string `json:"filename,omitempty"`
	// MIME type of the file to be created. Recommended for multi_part.
	ContentType string `json:"content_type,omitempty"`
	// When mode is multi_part, the number of parts you are uploading (1-1000).
	NumberOfParts *int32 `json:"number_of_parts,omitempty"`
	// When mode is external_url, the HTTPS URL of a publicly accessible file.
	ExternalURL string `json:"external_url,omitempty"`
}

// Create initiates the process of uploading a file to Notion.
// It returns a FileUpload object with a status of "pending" and an upload_url.
// See https://developers.notion.com/reference/create-file-upload
func (fuc *FileUploadClient) Create(ctx context.Context, requestBody *FileUploadCreateRequest) (*FileUpload, error) {
	// This assumes your apiClient.request method can take a struct, marshal it to JSON,
	// and set Content-Type to "application/json" by default if the last argument (contentTypeOverride) is empty.
	// Example call: apiClient.request(ctx, method, path, queryParams, requestStruct, "")
	res, err := fuc.apiClient.request(ctx, http.MethodPost, "file_uploads", nil, requestBody, "")
	if err != nil {
		return nil, err
	}

	defer func() {
		if errClose := res.Body.Close(); errClose != nil {
			log.Printf("FileUploadClient.Create: failed to close response body: %v", errClose)
		}
	}()

	if res.StatusCode != http.StatusOK {
		// Consider handling specific Notion API error structures here if desired
		return nil, fmt.Errorf("FileUploadClient.Create: unexpected status code: %d", res.StatusCode)
	}

	return handleFileUploadResponse(res)
}

// Send transmits file contents to Notion for a file upload initiated by Create.
// For this endpoint, Content-Type must be multipart/form-data.
// file is an io.Reader providing the content of the file (or part of the file).
// fileName is the name that will be associated with the file in the form data.
// partNumber is required if the upload was created with mode=multi_part, it specifies the chunk number.
// See https://developers.notion.com/reference/send-file-upload
func (fuc *FileUploadClient) Send(ctx context.Context, id FileUploadID, file io.Reader, fileName string, partNumber *int) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add file part
	formFile, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return fmt.Errorf("FileUploadClient.Send: failed to create form file: %w", err)
	}
	if _, err = io.Copy(formFile, file); err != nil {
		return fmt.Errorf("FileUploadClient.Send: failed to copy file to form: %w", err)
	}

	// Add part_number field if provided (required for multi_part)
	if partNumber != nil {
		if err = writer.WriteField("part_number", strconv.Itoa(*partNumber)); err != nil {
			return fmt.Errorf("FileUploadClient.Send: failed to write part_number field: %w", err)
		}
	}

	if err = writer.Close(); err != nil { // Finalizes the multipart body
		return fmt.Errorf("FileUploadClient.Send: failed to close multipart writer: %w", err)
	}

	uploadURL := fmt.Sprintf("file_uploads/%s/send", id.String())

	res, err := fuc.apiClient.request(ctx, http.MethodPost, uploadURL, nil, body, ContentTypeFormData)
	if err != nil {
		return fmt.Errorf("FileUploadClient.Send: request failed: %w", err)
	}

	defer func() {
		if errClose := res.Body.Close(); errClose != nil {
			log.Printf("FileUploadClient.Send: failed to close response body: %v", errClose)
		}
	}()

	if res.StatusCode != http.StatusOK {
		// Handle error response, potentially by reading the body for a Notion error object
		// For now, just returning a generic error
		// bodyBytes, _ := io.ReadAll(res.Body) // Example: read error body
		return fmt.Errorf("FileUploadClient.Send: unexpected status code: %d", res.StatusCode)
	}

	// The "Send a file upload" endpoint documentation doesn't specify a JSON response body.
	// It's used to transmit data. Success is indicated by HTTP 200 OK.
	// One would typically then retrieve the FileUpload object again to check its status.
	return nil
}

// FileUpload represents the Notion File Upload object.
// See https://developers.notion.com/reference/file-upload-object
type FileUpload struct {
	// Type of this object. Always "file_upload".
	Object ObjectType `json:"object"` // Assuming ObjectType is defined (e.g., type ObjectType string)
	// Identifier for the FileUpload.
	ID FileUploadID `json:"id"`
	// Date and time when the FileUpload was created.
	CreatedTime time.Time `json:"created_time"`
	// Date and time when the FileUpload was last updated.
	LastEditedTime time.Time `json:"last_edited_time"`
	// Date and time when the FileUpload will expire if not attached. Nullable.
	ExpiryTime *time.Time `json:"expiry_time,omitempty"`
	// Status of the file upload.
	Status FileUploadStatus `json:"status"`
	// Name of the file. Nullable.
	Filename string `json:"filename,omitempty"`
	// MIME type of the file. Nullable.
	ContentType string `json:"content_type,omitempty"`
	// Total size of the file in bytes. Nullable.
	ContentLength *int `json:"content_length,omitempty"`
	// URL to use for sending file contents (for pending uploads).
	UploadURL string `json:"upload_url,omitempty"`
	// URL to use to complete a multi-part file upload (for pending multi_part uploads).
	CompleteURL string `json:"complete_url,omitempty"`
	// Details on the success/failure of importing from an external URL.
	FileImportResult string `json:"file_import_result,omitempty"`
}

func handleFileUploadResponse(res *http.Response) (*FileUpload, error) {
	var response FileUpload
	err := json.NewDecoder(res.Body).Decode(&response)
	if err != nil {
		return nil, fmt.Errorf("handleFileUploadResponse: failed to decode json: %w", err)
	}
	return &response, nil
}

// --- Helper function to use Send with a file path ---

// SendFileByPath is a convenience wrapper around Send for uploading a file from a local path.
// partNumber is required if the upload was created with mode=multi_part.
func (fuc *FileUploadClient) SendFileByPath(ctx context.Context, id FileUploadID, filePath string, partNumber *int) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("SendFileByPath: failed to open file %s: %w", filePath, err)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Printf("SendFileByPath: failed to close file %s: %v", filePath, errClose)
		}
	}()

	// Extract filename from path for the multipart form
	// This is a simple extraction; more robust path manipulation might be needed
	// depending on os.PathSeparator or using filepath.Base.
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("SendFileByPath: failed to get file info for %s: %w", filePath, err)
	}
	fileName := info.Name()

	return fuc.Send(ctx, id, file, fileName, partNumber)
}
