package main

import (
	"archive/zip"
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"time"

	getter "github.com/hashicorp/go-getter"
	"github.com/hashicorp/terraform-svchost/disco"
	"github.com/hashicorp/terraform/addrs"
	"github.com/hashicorp/terraform/httpclient"
	"github.com/hashicorp/terraform/internal/getproviders"
	"github.com/hashicorp/terraform/internal/providercache"
	discovery "github.com/hashicorp/terraform/plugin/discovery"
	"github.com/hashicorp/terraform/tfdiags"
	"github.com/hashicorp/terraform/version"
	"github.com/mitchellh/cli"
)

var releaseHost = "https://releases.hashicorp.com"

var pluginDir = "./plugins"

type PackageCommand struct {
	ui cli.Ui
}

func (c *PackageCommand) Run(args []string) int {
	flags := flag.NewFlagSet("package", flag.ExitOnError)
	osPtr := flags.String("os", "", "Target operating system")
	archPtr := flags.String("arch", "", "Target CPU architecture")
	pluginDirPtr := flags.String("plugin-dir", "", "Path to custom plugins directory")
	err := flags.Parse(args)
	if err != nil {
		c.ui.Error(err.Error())
		return 1
	}

	osName := runtime.GOOS
	archName := runtime.GOARCH
	if *osPtr != "" {
		osName = *osPtr
	}
	if *archPtr != "" {
		archName = *archPtr
	}
	if *pluginDirPtr != "" {
		pluginDir = *pluginDirPtr
	}

	if flags.NArg() != 1 {
		c.ui.Error("Configuration filename is required")
		return 1
	}
	configFn := flags.Arg(0)

	config, err := LoadConfigFile(configFn)
	if err != nil {
		c.ui.Error(fmt.Sprintf("Failed to read config: %s", err))
		return 1
	}

	workDir, err := ioutil.TempDir("", "terraform-bundle")
	if err != nil {
		c.ui.Error(fmt.Sprintf("Could not create temporary dir: %s", err))
		return 1
	}
	defer os.RemoveAll(workDir)

	c.ui.Info(fmt.Sprintf("Fetching Terraform %s core package...", config.Terraform.Version))

	coreZipURL := c.coreURL(config.Terraform.Version, osName, archName)
	err = getter.Get(workDir, coreZipURL)
	if err != nil {
		c.ui.Error(fmt.Sprintf("Failed to fetch core package from %s: %s", coreZipURL, err))
		return 1
	}

	// get the list of required providers from the config
	reqs := make(map[addrs.Provider][]string)
	for name, provider := range config.Providers {
		var fqn addrs.Provider
		var diags tfdiags.Diagnostics
		if provider.Source != "" {
			fqn, diags = addrs.ParseProviderSourceString(provider.Source)
			if diags.HasErrors() {
				c.ui.Error(fmt.Sprintf("Invalid provider source string: %s", provider.Source))
				return 1
			}
		} else {
			fqn = addrs.NewDefaultProvider(name)
		}
		reqs[fqn] = provider.Versions
	}

	// set up the provider installer
	platform := getproviders.Platform{
		OS:   osName,
		Arch: archName,
	}
	installdir := providercache.NewDirWithPlatform(filepath.Join(workDir, "plugins"), platform)
	services := disco.New()
	services.SetUserAgent(httpclient.TerraformUserAgent(version.String()))
	var sources []getproviders.MultiSourceSelector
	sources = append(sources, getproviders.MultiSourceSelector{
		Source: getproviders.NewMemoizeSource(getproviders.NewRegistrySource(services)),
	})

	// if the local "./plugins" directory exists, include it as a potential source
	if _, err := os.Stat(pluginDir); err == nil {
		sources = append(sources, getproviders.MultiSourceSelector{
			Source: getproviders.NewFilesystemMirrorSource(pluginDir),
		})
	}

	installer := providercache.NewInstaller(installdir, getproviders.MultiSource(sources))
	err = c.ensureProviderVersions(installer, reqs)
	if err != nil {
		c.ui.Error(err.Error())
		return 1
	}

	// remove the selections.json file created by the provider installer
	os.Remove(filepath.Join(workDir, "plugins", "selections.json"))

	// If we get this far then our workDir now contains the union of the
	// contents of all the zip files we downloaded above. We can now create
	// our output file.
	outFn := c.bundleFilename(config.Terraform.Version, time.Now(), osName, archName)
	c.ui.Info(fmt.Sprintf("Creating %s ...", outFn))
	outF, err := os.OpenFile(outFn, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, os.ModePerm)
	if err != nil {
		c.ui.Error(fmt.Sprintf("Failed to create %s: %s", outFn, err))
		return 1
	}
	outZ := zip.NewWriter(outF)
	defer func() {
		err := outZ.Close()
		if err != nil {
			c.ui.Error(fmt.Sprintf("Failed to close %s: %s", outFn, err))
			os.Exit(1)
		}
		err = outF.Close()
		if err != nil {
			c.ui.Error(fmt.Sprintf("Failed to close %s: %s", outFn, err))
			os.Exit(1)
		}
	}()

	// recursively walk the workDir to get a list of all binary filepaths
	err = filepath.Walk(workDir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			fn := info.Name()

			// provider plugins need to be created in the same relative directory structure
			absPath, _ := filepath.Abs(path)
			relPath, _ := filepath.Rel(workDir, absPath)
			relZip, err := outZ.Create(relPath)

			if err != nil {
				return fmt.Errorf("Failed to add zip entry for %s: %s", fn, err)
			}

			r, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("Failed to open %s: %s", fn, err)
			}
			_, err = io.Copy(relZip, r)
			if err != nil {
				return fmt.Errorf("Failed to write %s to bundle: %s", fn, err)
			}
			return nil
		})

	if err != nil {
		c.ui.Error(err.Error())
		return 1
	}
	c.ui.Info("All done!")

	return 0
}

func (c *PackageCommand) bundleFilename(version discovery.VersionStr, time time.Time, osName, archName string) string {
	time = time.UTC()
	return fmt.Sprintf(
		"terraform_%s-bundle%04d%02d%02d%02d_%s_%s.zip",
		version,
		time.Year(), time.Month(), time.Day(), time.Hour(),
		osName, archName,
	)
}

func (c *PackageCommand) coreURL(version discovery.VersionStr, osName, archName string) string {
	return fmt.Sprintf(
		"%s/terraform/%s/terraform_%s_%s_%s.zip",
		releaseHost, version, version, osName, archName,
	)
}

func (c *PackageCommand) Synopsis() string {
	return "Produces a bundle archive"
}

func (c *PackageCommand) Help() string {
	return `Usage: terraform-bundle package [options] <config-file>

Uses the given bundle configuration file to produce a zip file in the
current working directory containing a Terraform binary along with zero or
more provider plugin binaries.

Options:
  -os=name    		Target operating system the archive will be built for. Defaults
              		to that of the system where the command is being run.

  -arch=name  		Target CPU architecture the archive will be built for. Defaults
					to that of the system where the command is being run.
					  
  -plugin-dir=path 	The path to the custom plugins directory. Defaults to "./plugins".

The resulting zip file can be used to more easily install Terraform and
a fixed set of providers together on a server, so that Terraform's provider
auto-installation mechanism can be avoided.

To build an archive for Terraform Enterprise, use:
  -os=linux -arch=amd64

Note that the given configuration file is a format specific to this command,
not a normal Terraform configuration file. The file format looks like this:

  terraform {
    # Version of Terraform to include in the bundle. An exact version number
	# is required.
    version = "0.13.0"
  }

  # Define which provider plugins are to be included
  providers {
    # Include the newest "aws" provider version in the 1.0 series.
    aws = {
		versions = ["~> 1.0"]
	}

    # Include both the newest 1.0 and 2.0 versions of the "google" provider.
    # Each item in these lists allows a distinct version to be added. If the
	# two expressions match different versions then _both_ are included in
	# the bundle archive.
	google = {
		versions = ["~> 1.0", "~> 2.0"]
	}

	# Include a custom plugin to the bundle. Will search for the plugin in the 
	# plugins directory, and package it with the bundle archive. Plugin must 
	# have a name of the form: terraform-provider-*-v*, and must be built with
	# the operating system and architecture that terraform enterprise is running,
	# e.g. linux and amd64.
	# See the README for more information on the source attribute and plugin
	# directory layout.
	customplugin = {
		versions = ["0.1"]
		source = "example.com/myorg/customplugin"
	}
  }

`
}

// ensureProviderVersions is a wrapper around
// providercache.EnsureProviderVersions which allows installing multiple
// versions of a given provider.
func (c *PackageCommand) ensureProviderVersions(installer *providercache.Installer, reqs map[addrs.Provider][]string) error {
	mode := providercache.InstallNewProvidersOnly
	evts := &providercache.InstallerEvents{
		ProviderAlreadyInstalled: func(provider addrs.Provider, selectedVersion getproviders.Version) {
			c.ui.Info(fmt.Sprintf("- Using previously-installed %s v%s", provider.ForDisplay(), selectedVersion))
		},
		QueryPackagesBegin: func(provider addrs.Provider, versionConstraints getproviders.VersionConstraints) {
			if len(versionConstraints) > 0 {
				c.ui.Info(fmt.Sprintf("- Finding %s versions matching %q...", provider.ForDisplay(), getproviders.VersionConstraintsString(versionConstraints)))
			} else {
				c.ui.Info(fmt.Sprintf("- Finding latest version of %s...", provider.ForDisplay()))
			}
		},
		FetchPackageBegin: func(provider addrs.Provider, version getproviders.Version, location getproviders.PackageLocation) {
			c.ui.Info(fmt.Sprintf("- Installing %s v%s...", provider.ForDisplay(), version))
		},
		QueryPackagesFailure: func(provider addrs.Provider, err error) {
			c.ui.Error(fmt.Sprintf("Could not retrieve the list of available versions for provider %s: %s.", provider.ForDisplay(), err))
		},
		FetchPackageFailure: func(provider addrs.Provider, version getproviders.Version, err error) {
			c.ui.Error(fmt.Sprintf("Error while installing %s v%s: %s.", provider.ForDisplay(), version, err))
		},
	}

	ctx := evts.OnContext(context.TODO())
	for provider, versions := range reqs {
		for _, constraint := range versions {
			req := make(getproviders.Requirements, 1)
			cstr, err := getproviders.ParseVersionConstraints(constraint)
			if err != nil {
				return err
			}
			req[provider] = cstr
			_, err = installer.EnsureProviderVersions(ctx, req, mode)
			if err != nil {
				return err
			}
		}
	}

	return nil
}
