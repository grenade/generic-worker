package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/taskcluster/httpbackoff"
	"github.com/taskcluster/taskcluster-client-go/awsprovisioner"
)

func queryUserData() (*UserData, error) {
	// http://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-metadata.html#instancedata-user-data-retrieval
	resp, _, err := httpbackoff.Get("http://169.254.169.254/latest/user-data")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	userData := new(UserData)
	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(userData)
	return userData, err
}

func queryMetaData(url, name string) (string, error) {
	// http://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-metadata.html#instancedata-data-retrieval
	// call http://169.254.169.254/latest/meta-data/instance-id with httpbackoff
	resp, _, err := httpbackoff.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	content, err := ioutil.ReadAll(resp.Body)
	fmt.Println(name + ": " + string(content))
	return string(content), err
}

func queryInstanceName() (string, error) {
	return queryMetaData("http://169.254.169.254/latest/meta-data/instance-id", "Instance name")
}

func queryPublicIP() (string, error) {
	return queryMetaData("http://169.254.169.254/latest/meta-data/public-ipv4", "Public IP")
}

type UserData struct {
	Data                interface{} `json:"data"`
	Capacity            int         `json:"capacity"`
	WorkerType          string      `json:"workerType"`
	ProvisionerId       string      `json:"provisionerId"`
	Region              string      `json:"region"`
	InstanceType        string      `json:"instanceType"`
	LaunchSpecGenerated time.Time   `json:"launchSpecGenerated"`
	WorkerModified      time.Time   `json:"workerModified"`
	ProvisionerBaseUrl  string      `json:"provisionerBaseUrl"`
	SecurityToken       string      `json:"securityToken"`
}

type Secrets struct {
	GenericWorker struct {
		Config json.RawMessage `json:"config"`
	} `json:"generic-worker"`
	Files []File `json:"files"`
}

type File struct {
	Description string `json:"description"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	Format      string `json:"format"`
}

func (f File) Extract() error {
	switch f.Format {
	case "file":
		return f.ExtractFile()
	case "zip":
		return f.ExtractZip()
	default:
		return errors.New("Unknown file format " + f.Format + " in worker type secret")
	}
}

func (f File) ExtractFile() error {
	switch f.Encoding {
	case "base64":
		data, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return err
		}
		return ioutil.WriteFile(f.Path, data, 0777)
	default:
		return errors.New("Unsupported encoding " + f.Encoding + " for file secret in worker type definition")
	}
}

func (f File) ExtractZip() error {
	switch f.Encoding {
	case "base64":
		data, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return err
		}
		return Unzip(data, f.Path)
	default:
		return errors.New("Unsupported encoding " + f.Encoding + " for zip secret in worker type definition")
	}
}

// This is a modified version of
// http://stackoverflow.com/questions/20357223/easy-way-to-unzip-file-with-golang
// to work with in memory zip, rather than a file
func Unzip(b []byte, dest string) error {
	br := bytes.NewReader(b)
	r, err := zip.NewReader(br, int64(len(b)))
	if err != nil {
		return err
	}

	os.MkdirAll(dest, 0755)

	// Closure to address file descriptors issue with all the deferred .Close() methods
	extractAndWriteFile := func(f *zip.File) error {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer func() {
			if err := rc.Close(); err != nil {
				panic(err)
			}
		}()

		path := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
		} else {
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return err
			}
			defer func() {
				if err := f.Close(); err != nil {
					panic(err)
				}
			}()

			_, err = io.Copy(f, rc)
			if err != nil {
				return err
			}
		}
		return nil
	}

	for _, f := range r.File {
		err := extractAndWriteFile(f)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Config) updateConfigWithAmazonSettings() error {
	userData, err := queryUserData()
	if err != nil {
		return err
	}
	instanceName, err := queryInstanceName()
	if err != nil {
		return err
	}
	publicIP, err := queryPublicIP()
	if err != nil {
		return err
	}
	c.ProvisionerId = userData.ProvisionerId
	awsprov := awsprovisioner.AwsProvisioner{
		Authenticate: false,
		BaseURL:      userData.ProvisionerBaseUrl,
	}
	secToken, _, getErr := awsprov.GetSecret(userData.SecurityToken)
	// remove secrets even if we couldn't retrieve them!
	_, removeErr := awsprov.RemoveSecret(userData.SecurityToken)
	if getErr != nil {
		return err
	}
	if removeErr != nil {
		return removeErr
	}
	c.AccessToken = secToken.Credentials.AccessToken
	c.ClientId = secToken.Credentials.ClientID
	c.Certificate = secToken.Credentials.Certificate
	c.WorkerGroup = userData.Region
	c.WorkerId = instanceName
	c.PublicIP = net.ParseIP(publicIP)
	c.WorkerType = userData.WorkerType
	secrets := new(Secrets)
	json.Unmarshal(secToken.Data, secrets)
	if err != nil {
		return err
	}

	// Now overlay existing config with values in secrets
	json.Unmarshal(secrets.GenericWorker.Config, c)
	if err != nil {
		return err
	}

	fmt.Printf("\n\nConfig\n\n%#v\n\n", c)

	// Now put secret files in place...
	for _, f := range secrets.Files {
		err := f.Extract()
		if err != nil {
			return err
		}
	}
	return nil
}
