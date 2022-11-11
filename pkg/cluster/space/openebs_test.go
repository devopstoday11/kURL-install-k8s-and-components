package clusterspace

import (
	"context"
	"io"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"k8s.io/utils/pointer"
)

func Test_deleteTmpPVCs(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	kcli := fake.NewSimpleClientset()
	ochecker := OpenEBSChecker{
		deletePVTimeout: 20 * time.Second,
		kcli:            kcli,
		log:             logger,
	}

	// empty list of pvcs
	err := ochecker.deleteTmpPVCs(context.Background(), []*corev1.PersistentVolumeClaim{})
	if err != nil {
		t.Errorf("error deleting empty list of pvs: %s", err)
	}

	// delete non existing pvc
	pvcs := []*corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "pvc0",
				Namespace: "namespace",
			},
		},
	}
	if err := ochecker.deleteTmpPVCs(context.Background(), pvcs); err != nil {
		t.Errorf("unexpected error deleting empty list of pvs: %s", err)
	}

	// nil claim ref for a pv
	var objs []runtime.Object
	for _, pvc := range pvcs {
		objs = append(objs, pvc)
	}
	objs = append(objs, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pv",
		},
	})
	ochecker.kcli = fake.NewSimpleClientset(objs...)
	if err := ochecker.deleteTmpPVCs(context.Background(), pvcs); err != nil {
		t.Errorf("unexpected error deleting with pv without claim ref: %s", err)
	}

	// different pv claimref
	objs = []runtime.Object{}
	for _, pvc := range pvcs {
		objs = append(objs, pvc)
	}
	objs = append(objs, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pv",
		},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{
				Name: "abc",
			},
		},
	})
	ochecker.kcli = fake.NewSimpleClientset(objs...)
	if err := ochecker.deleteTmpPVCs(context.Background(), pvcs); err != nil {
		t.Errorf("unexpected error deleting with unrelated pv: %s", err)
	}

	// cancel context before the pv is removed
	objs = []runtime.Object{}
	for _, pvc := range pvcs {
		objs = append(objs, pvc)
	}
	objs = append(objs, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pv",
		},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{
				Name: "pvc0",
			},
		},
	})
	ochecker.kcli = fake.NewSimpleClientset(objs...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(3 * time.Second)
		cancel()
	}()

	if err := ochecker.deleteTmpPVCs(ctx, pvcs); err == nil || err.Error() != "context cancelled" {
		t.Errorf("context cancelled error not showing up")
	}

	// happy path, pv disappear after a while
	objs = []runtime.Object{}
	for _, pvc := range pvcs {
		objs = append(objs, pvc)
	}
	objs = append(objs, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pv",
		},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{
				Name: "pvc0",
			},
		},
	})
	ochecker.kcli = fake.NewSimpleClientset(objs...)

	go func() {
		time.Sleep(10 * time.Second)
		if err := ochecker.kcli.CoreV1().PersistentVolumes().Delete(
			context.Background(), "pv", metav1.DeleteOptions{},
		); err != nil {
			t.Errorf("failed to delete test pv: %s", err)
		}
	}()

	if err := ochecker.deleteTmpPVCs(context.Background(), pvcs); err != nil {
		t.Errorf("unexpected error deleting pvs: %s", err)
	}

	// timeout deleting pv
	objs = []runtime.Object{}
	for _, pvc := range pvcs {
		objs = append(objs, pvc)
	}
	objs = append(objs, &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pv",
		},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{
				Name: "pvc0",
			},
		},
	})
	ochecker.kcli = fake.NewSimpleClientset(objs...)
	ochecker.deletePVTimeout = 2 * time.Second
	if err := ochecker.deleteTmpPVCs(context.Background(), pvcs); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("timeout error did now show up")
	}
}

func Test_nodeIsScheduleable(t *testing.T) {
	for _, tt := range []struct {
		name        string
		err         bool
		annotations map[string]string
	}{
		{
			name: "not ready",
			err:  true,
			annotations: map[string]string{
				"node.kubernetes.io/not-ready": "NoExecute",
			},
		},
		{
			name: "multiple not ready annotations",
			err:  true,
			annotations: map[string]string{
				"node.kubernetes.io/not-ready":              "NoExecute",
				"node.cloudprovider.kubernetes.io/shutdown": "NoExecute",
				"node.kubernetes.io/unschedulable":          "NoExecute",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			node := corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: tt.annotations,
				},
			}

			ochecker := OpenEBSChecker{}
			err := ochecker.nodeIsSchedulable(node)
			if err != nil {
				if !tt.err {
					t.Errorf("unexpected error: %s", err)
				}
				return
			}

			if tt.err {
				t.Errorf("expecting error nil received instead")
			}
		})
	}
}

func Test_bulidTmpPVC(t *testing.T) {
	for _, tt := range []struct {
		name         string
		nodeName     string
		dstSC        string
		expectedName string
		expectedSpec corev1.PersistentVolumeClaimSpec
	}{
		{
			name:         "happy path",
			nodeName:     "node0",
			expectedName: "disk-free-node0-",
			dstSC:        "xyz",
			expectedSpec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("xyz"),
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Mi"),
					},
				},
			},
		},
		{
			name:         "very long host name",
			nodeName:     "this-is-a-relly-long-host-name-and-this-should-be-trimmed",
			expectedName: "disk-free-this-is-a-relly-long-and-this-should-be-trimmed-",
			dstSC:        "default",
			expectedSpec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("default"),
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Mi"),
					},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ochecker := OpenEBSChecker{
				dstSC: tt.dstSC,
			}
			pvc := ochecker.buildTmpPVC(tt.nodeName)

			if !strings.HasPrefix(pvc.Name, tt.expectedName) {
				t.Errorf("expected name to have prefix %s, %s received", tt.expectedName, pvc.Name)
			}

			if diff := cmp.Diff(tt.expectedSpec, pvc.Spec); diff != "" {
				t.Errorf("unexpected return: %s", diff)
			}
		})
	}
}

func Test_parseDFContainerOutput(t *testing.T) {
	for _, tt := range []struct {
		name         string
		content      []byte
		err          string
		expectedFree int64
		expectedUsed int64
	}{
		{
			name:    "empty",
			content: []byte(``),
			err:     "failed to locate free space info in pod log",
		},
		{
			name:    "invalid return",
			content: []byte(`...---...---...<<<<>>>>>>`),
			err:     "failed to locate free space info in pod log",
		},
		{
			name: "human readable return",
			content: []byte(`Filesystem      Size  Used Avail Use% Mounted on
/dev/sda2        59G   49G  6.9G  88% /data`),
			err: `failed to parse "6.9G" as available spac`,
		},
		{
			name: "human readable return (used)",
			content: []byte(`Filesystem      Size  Used Avail Use% Mounted on
/dev/sda2        59G   49G  100  88% /data`),
			err: `failed to parse "49G" as used spac`,
		},
		{
			name: "strange mount point",
			content: []byte(`Filesystem      Size  Used Avail Use% Mounted on
/dev/sda2        59G   49G  6.9G  88% /`),
			err: "failed to locate free space info in pod log",
		},
		{
			name:    "line ending with /data",
			content: []byte(`something weird /data`),
			err:     "failed to locate free space info in pod log",
		},
		{
			name:    "line ending with /data and five words",
			content: []byte(`this is a failure /data`),
			err:     `failed to parse "a" as available space`,
		},
		{
			name: "happy path",
			content: []byte(`Filesystem       1B-blocks        Used  Available Use% Mounted on
/dev/sda2      63087357952 52521754624 7327760384  88% /data`),
			expectedFree: 7327760384,
			expectedUsed: 52521754624,
		},
		{
			name: "happy path with an empty line",
			content: []byte(`Filesystem       1B-blocks        Used  Available Use% Mounted on

/dev/sda2      63087357952 52521754624 7327760384  88% /data`),
			expectedFree: 7327760384,
			expectedUsed: 52521754624,
		},
		{
			name: "happy path (prefixes)",
			content: []byte(`Filesystem       1B-blocks        Used  Available Use% Mounted on
some prefixes go in here /dev/sda2      63087357952 52521754624 7327760384  88% /data`),
			expectedFree: 7327760384,
			expectedUsed: 52521754624,
		},
		{
			name: "happy path (oracle linux output)",
			content: []byte(`Filesystem       1B-blocks       Used   Available Use% Mounted on
/dev/xvda1     85886742528 8500056064 77386686464  10% /data`),
			expectedFree: 77386686464,
			expectedUsed: 8500056064,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ochecker := OpenEBSChecker{}
			free, used, err := ochecker.parseDFContainerOutput(tt.content)
			if err != nil {
				if len(tt.err) == 0 {
					t.Errorf("unexpected error: %s", err)
				} else if !strings.Contains(err.Error(), tt.err) {
					t.Errorf("expecting %q, %q received instead", tt.err, err)
				}
				return
			}

			if len(tt.err) > 0 {
				t.Errorf("expecting error %q, nil received instead", tt.err)
			}

			if !reflect.DeepEqual(tt.expectedFree, free) {
				t.Errorf("expected free %v, received %v", tt.expectedFree, free)
			}
			if !reflect.DeepEqual(tt.expectedUsed, used) {
				t.Errorf("expected used %v, received %v", tt.expectedUsed, used)
			}
		})
	}
}

func Test_parseFstabContainerOutput(t *testing.T) {
	for _, tt := range []struct {
		name     string
		content  []byte
		err      string
		expected []string
	}{
		{
			name: "oracle linux amazon",
			content: []byte(`#
UUID=d8605abb-d6cd-4a46-a657-b6bd206da2ab     /           xfs    defaults,noatime  1   1`),
			expected: []string{"/"},
		},
		{
			name: "local vm ubuntu 22.04",
			content: []byte(`# /etc/fstab: static file system information.
#
# Use 'blkid' to print the universally unique identifier for a
# device; this may be used with UUID= as a more robust way to name devices
# that works even if disks are added and removed. See fstab(5).
#
# <file system> <mount point>   <type>  <options>       <dump>  <pass>
# / was on /dev/sda2 during curtin installation
/dev/disk/by-uuid/ba03d262-e4fc-4bb2-8e2f-4e654315da3a / ext4 defaults 0 1`),
			expected: []string{"/"},
		},
		{
			name: "multiple volume mounts",
			content: []byte(`# /etc/fstab: static file system information.
#
# Use 'blkid' to print the universally unique identifier for a
# device; this may be used with UUID= as a more robust way to name devices
# that works even if disks are added and removed. See fstab(5).
#
# <file system> <mount point>   <type>  <options>       <dump>  <pass>
# / was on /dev/sda2 during curtin installation
/dev/disk/by-uuid/ba03d262-e4fc-4bb2-8e2f-4e654315da3a / ext4 defaults 0 1
/dev/disk/by-uuid/4bb2-8e2f-4e654315da3a /opt ext4 defaults 0 1`),
			expected: []string{"/", "/opt"},
		},
		{
			name:    "empty",
			content: []byte(``),
			err:     "failed to locate any mount point",
		},
		{
			name: "with uuid and with none",
			content: []byte(`# /etc/fstab: static file system information.
#
# <file system> <mount point>   <type>  <options>       <dump>  <pass>

proc  /proc  proc  defaults  0  0
# /dev/sda5
UUID=be35a709-c787-4198-a903-d5fdc80ab2f8  /  ext3  relatime,errors=remount-ro  0  1
# /dev/sda6
UUID=cee15eca-5b2e-48ad-9735-eae5ac14bc90  none  swap  sw  0  0

/dev/scd0  /media/cdrom0  udf,iso9660  user,noauto,exec,utf8  0  0`),
			expected: []string{"/proc", "/", "/media/cdrom0"},
		},
		{
			name: "with repeated mount point",
			content: []byte(`# FAT ~ Linux calls FAT file systems vfat)
# /dev/hda1
UUID=12102C02102CEB83  /media/windows  vfat auto,users,uid=1000,gid=100,dmask=027,fmask=137,utf8  0  0

# NTFS ~ Use ntfs-3g for write access (rw) 
# /dev/hda1
UUID=12102C02102CEB83  /media/windows  ntfs-3g  auto,users,uid=1000,gid=100,dmask=027,fmask=137,utf8  0  0

# Zip Drives ~ Linux recognizes ZIP drives as sdx'''4'''

# Separate Home
# /dev/sda7
UUID=413eee0c-61ff-4cb7-a299-89d12b075093  /home  ext3  nodev,nosuid,relatime  0  2

# Data partition
# /dev/sda8
UUID=3f8c5321-7181-40b3-a867-9c04a6cd5f2f  /media/data  ext3  relatime,noexec  0  2

# Samba
//server/share  /media/samba  cifs  user=user,uid=1000,gid=100  0  0
# "Server" = Samba server (by IP or name if you have an entry for the server in your hosts file
# "share" = name of the shared directory
# "user" = your samba user
# This set up will ask for a password when mounting the samba share. If you do not want to enter a password, use a credentials file.
# replace "user=user" with "credentials=/etc/samba/credentials" In the credentials file put two lines
# username=user
# password=password
# make the file owned by root and ro by root (sudo chown root.root /etc/samba/credentials && sudo chmod 400 /etc/samba/credentials)

# NFS
Server:/share  /media/nfs  nfs  rsize=8192 and wsize=8192,noexec,nosuid
# "Server" = Samba server (by IP or name if you have an entry for the server in your hosts file
# "share" = name of the shared directory

#SSHFS
sshfs#user@server:/share  fuse  user,allow_other  0  0
# "Server" = Samba server (by IP or name if you have an entry for the server in your hosts file
# "share" = name of the shared directory`),
			expected: []string{"/media/windows", "/home", "/media/data", "/media/samba", "/media/nfs"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ochecker := OpenEBSChecker{}
			output, err := ochecker.parseFstabContainerOutput(tt.content)
			if err != nil {
				if len(tt.err) == 0 {
					t.Errorf("unexpected error: %s", err)
				} else if !strings.Contains(err.Error(), tt.err) {
					t.Errorf("expecting %q, %q received instead", tt.err, err)
				}
				return
			}

			if len(tt.err) > 0 {
				t.Errorf("expecting error %q, nil received instead", tt.err)
			}

			if !reflect.DeepEqual(tt.expected, output) {
				t.Errorf("expected %v, received %v", tt.expected, output)
			}
		})
	}
}

func Test_basePath(t *testing.T) {
	for _, tt := range []struct {
		name     string
		expected string
		err      string
		dstSC    string
		objs     []runtime.Object
	}{
		{
			name:  "no storage class found",
			dstSC: "does-not-exist",
			err:   `class: storageclasses.storage.k8s.io "does-not-exist" not found`,
			objs:  []runtime.Object{},
		},
		{
			name:  "no annotation",
			dstSC: "default",
			err:   "annotation not found in storage class",
			objs: []runtime.Object{
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
				},
			},
		},
		{
			name:  "invalid configuration",
			dstSC: "default",
			err:   "failed to parse openebs config annotation",
			objs: []runtime.Object{
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
						Annotations: map[string]string{
							"cas.openebs.io/config": "...---...<<>>",
						},
					},
				},
			},
		},
		{
			name:  "no base path config",
			dstSC: "default",
			err:   "openebs base path not defined in the storage class",
			objs: []runtime.Object{
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
						Annotations: map[string]string{
							"cas.openebs.io/config": "- name: abc\n  value: cba",
						},
					},
				},
			},
		},
		{
			name:  "empty base path",
			dstSC: "default",
			err:   "invalid opeenbs base path",
			objs: []runtime.Object{
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
						Annotations: map[string]string{
							"cas.openebs.io/config": "- name: BasePath\n  value: \"\"",
						},
					},
				},
			},
		},
		{
			name:  "invalid base path",
			dstSC: "default",
			err:   "invalid opeenbs base path",
			objs: []runtime.Object{
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
						Annotations: map[string]string{
							"cas.openebs.io/config": "- name: BasePath\n  value: invalid",
						},
					},
				},
			},
		},
		{
			name:     "happy path",
			dstSC:    "default",
			expected: "/var/local",
			objs: []runtime.Object{
				&storagev1.StorageClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
						Annotations: map[string]string{
							"cas.openebs.io/config": "- name: BasePath\n  value: /var/local",
						},
					},
				},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakecli := fake.NewSimpleClientset(tt.objs...)
			ochecker := OpenEBSChecker{
				kcli:  fakecli,
				dstSC: tt.dstSC,
			}

			bpath, err := ochecker.basePath(context.Background())
			if err != nil {
				if len(tt.err) == 0 {
					t.Errorf("unexpected error: %s", err)
				} else if !strings.Contains(err.Error(), tt.err) {
					t.Errorf("expecting %q, %q received instead", tt.err, err)
				}
				return
			}

			if len(tt.err) > 0 {
				t.Errorf("expecting error %q, nil received instead", tt.err)
			}

			if bpath != tt.expected {
				t.Errorf("expected %v, received %v", tt.expected, bpath)
			}
		})
	}
}

func Test_buildJob(t *testing.T) {
	nname := "this-is-a-very-long-node-name-this-will-extrapolate-the-limit"
	ochecker := OpenEBSChecker{image: "myimage:latest"}
	job := ochecker.buildJob(context.Background(), nname, "/var/local", "tmppvc")

	// check that the job name is within boundaries
	if len(job.Name) > 63 {
		t.Errorf("job name is bigger than the limit (63)")
	}

	// check that the job will run in the default namespace
	if job.Namespace != "default" {
		t.Errorf("job not going to run in the default namespace: %s", job.Namespace)
	}

	// check that the job is being scheduled in the right node
	affinity := job.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if affinity.NodeSelectorTerms[0].MatchExpressions[0].Values[0] != nname {
		t.Errorf("node has not be set to be scheduled in the node")
	}

	// assure that the temp pvc is among the volumes
	var mountName string
	for _, vol := range job.Spec.Template.Spec.Volumes {
		pvc := vol.VolumeSource.PersistentVolumeClaim
		if pvc == nil || pvc.ClaimName != "tmppvc" {
			continue
		}

		mountName = vol.Name
		break
	}
	if mountName == "" {
		t.Errorf("temp pvc not found among volumes")
	}

	// assure that the temp pvc is mounted
	var found bool
	for _, vm := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		if vm.Name == mountName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("temp pvc not mounted")
	}

	// assure all containers are using the image
	for i, cont := range job.Spec.Template.Spec.Containers {
		if cont.Image != "myimage:latest" {
			t.Errorf("image not set in container %d: %s", i, cont.Image)
		}
	}
}

func Test_hasEnoughSpace(t *testing.T) {
	for _, tt := range []struct {
		name     string
		volume   OpenEBSVolume
		reserved int64
		hasSpace bool
		free     int64
	}{
		{
			name: "empty open ebs volume",
		},
		{
			name:     "enough space (different mount point)",
			reserved: 99,
			free:     100,
			hasSpace: true,
			volume: OpenEBSVolume{
				Free:       100,
				Used:       0,
				RootVolume: false,
			},
		},
		{
			name:     "enough space (same mount point)",
			reserved: 50,
			free:     85,
			hasSpace: true,
			volume: OpenEBSVolume{
				Free:       100,
				Used:       0,
				RootVolume: true,
			},
		},
		{
			name:     "not enough space (same mount point)",
			reserved: 86,
			free:     85,
			hasSpace: false,
			volume: OpenEBSVolume{
				Free:       100,
				Used:       0,
				RootVolume: true,
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ochecker := OpenEBSChecker{}
			free, hasSpace := ochecker.hasEnoughSpace(tt.volume, tt.reserved)

			if hasSpace != tt.hasSpace {
				t.Errorf("expected hasSpace to be %v, %v received instead", tt.hasSpace, hasSpace)
			}

			if free != tt.free {
				t.Errorf("expected free to be %v, %v received instead", tt.free, free)
			}
		})
	}
}

func TestNewOpenEBSChecker(t *testing.T) {
	// test empty logger
	_, err := NewOpenEBSChecker(&rest.Config{}, nil, "image", "src", "dst")
	if err == nil || err.Error() != "no logger provided" {
		t.Errorf("expected failure creating object: %v", err)
	}

	logger := log.New(io.Discard, "", 0)

	// test empty image
	_, err = NewOpenEBSChecker(&rest.Config{}, logger, "", "src", "dst")
	if err == nil || err.Error() != "empty image" {
		t.Errorf("expected failure creating object: %v", err)
	}

	// test src storage class
	_, err = NewOpenEBSChecker(&rest.Config{}, logger, "image", "", "dst")
	if err == nil || err.Error() != "empty source storage class" {
		t.Errorf("expected failure creating object: %v", err)
	}

	// test empty dst sc
	_, err = NewOpenEBSChecker(&rest.Config{}, logger, "image", "src", "")
	if err == nil || err.Error() != "empty destination storage class" {
		t.Errorf("expected failure creating object: %v", err)
	}

	// happy path
	_, err = NewOpenEBSChecker(&rest.Config{}, logger, "image", "src", "dst")
	if err != nil {
		t.Errorf("unexpected failure creating object: %v", err)
	}
}
