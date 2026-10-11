package hyperv

import (
	"context"
	"fmt"
)

// SeedISO writes a cloud-init NoCloud seed (an ISO labelled "cidata") from
// the files in srcDir (user-data, meta-data, network-config). It uses
// Windows' own IMAPI2 image writer, so no ISO tool is needed in WSL. Joliet
// keeps the hyphenated file names cloud-init looks for.
func SeedISO(ctx context.Context, srcDir, isoPath string) error {
	_, err := Run(ctx, fmt.Sprintf(`
if (-not ('MeshISOFile' -as [type])) {
  $cp = New-Object CodeDom.Compiler.CompilerParameters
  $cp.CompilerOptions = '/unsafe'
  Add-Type -CompilerParameters $cp -TypeDefinition @'
public class MeshISOFile {
  public unsafe static void Create(string path, object stream, int blockSize, int totalBlocks) {
    int read = 0;
    byte[] buf = new byte[blockSize];
    var ptr = (System.IntPtr)(&read);
    var input = stream as System.Runtime.InteropServices.ComTypes.IStream;
    using (var output = System.IO.File.Create(path)) {
      while (totalBlocks-- > 0) { input.Read(buf, blockSize, ptr); output.Write(buf, 0, read); }
    }
  }
}
'@
}
$fsi = New-Object -ComObject IMAPI2FS.MsftFileSystemImage
$fsi.FileSystemsToCreate = 3
$fsi.VolumeName = 'cidata'
$fsi.Root.AddTree(%s, $false)
$img = $fsi.CreateResultImage()
[MeshISOFile]::Create(%s, $img.ImageStream, $img.BlockSize, $img.TotalBlocks)
`, Quote(srcDir), Quote(isoPath)))
	if err != nil {
		return fmt.Errorf("seed ISO %s: %w", isoPath, err)
	}
	return nil
}
