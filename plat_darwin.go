package main

import (
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/dchest/uniuri"
)

func deleteHomeDir(path string, user string) error {
	log.Println("Removing home directory '" + path + "'...")
	err := os.RemoveAll(path)
	if err != nil {
		log.Println("WARNING: could not delete directory '" + path + "'")
		log.Printf("%v", err)
		return err
	}
	return nil
}

func createNewTaskUser() error {
	// username can only be 20 chars, uuids are too long, therefore
	// use prefix (5 chars) plus seconds since epoch (10 chars)
	userName := "Task_" + strconv.Itoa((int)(time.Now().Unix()))
	password := generatePassword()
	TaskUser = OSUser{
		HomeDir:  "/Users/" + userName,
		Name:     userName,
		Password: password,
	}
	err := (&TaskUser).createNewOSUser()
	if err != nil {
		return err
	}
	// store password
	err = ioutil.WriteFile(filepath.Join(TaskUser.HomeDir, "_Passw0rd"), []byte(TaskUser.Password), 0666)
	if err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(TaskUser.HomeDir, "public", "logs"), 0777)
}

func (user *OSUser) createNewOSUser() error {

	createUserScript := `
		set -eu
		username="${0}"
		homedir="${1}"
		fullname="${2}"
		password="${3}"
		echo "Creating user '${username}' with home directory '${homedir}' and password '${password}'..."
		maxid=$(dscl . -list '/Users' 'UniqueID' | awk '{print $2}' | sort -ug | tail -1)
		newid=$((maxid+1))
		dscl . -create "/Users/${username}"
		dscl . -create "/Users/${username}" 'UserShell' '/bin/bash'
		dscl . -create "/Users/${username}" 'RealName' "${fullname}"
		dscl . -create "/Users/${username}" 'UniqueID' "${newid}"
		dscl . -passwd "/Users/${username}" "${password}" 
		staff="$(dscl . -read /Groups/staff | awk '($1 == "PrimaryGroupID:") { print $2 }')"
		dscl . -create "/Users/${username}" 'PrimaryGroupID' "${staff}"
		dscl . -create "/Users/${username}" 'NFSHomeDirectory' "${homedir}"
		cp -R '/System/Library/User Template/English.lproj' "${homedir}"
		chown -R "${username}:staff" "${homedir}"
		echo "User '${username}' created."
	`

	out, err := exec.Command("sudo", "/bin/bash", "-c", createUserScript, user.Name, user.HomeDir, user.Name+" User", user.Password).Output()
	log.Println(string(out))
	return err
}

// Uses [A-Za-z0-9] characters (default set) to avoid strange escaping problems
// that could potentially affect security. Prefixed with `pWd0_` to ensure
// password contains a special character (_), lowercase and uppercase letters,
// and a number. This is useful if the OS has a strict password policy
// requiring all of these. The total password length is 29 characters (24 of
// which are random). 29 characters should not be too long for the OS. The 24
// random characters of [A-Za-z0-9] provide (26+26+10)^24 possible permutations
// (approx 143 bits of randomness). Randomisation is not seeded, so results
// should not be reproducible.
func generatePassword() string {
	return "pWd0_" + uniuri.NewLen(24)
}
