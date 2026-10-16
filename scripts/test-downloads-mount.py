"""Mount parser checks; the image smoke test exercises the real entrypoint."""
import subprocess
from pathlib import Path
root=Path(__file__).resolve().parents[1]
cases=[
 ('container directory','1 0 0:1 / / rw - overlay overlay rw\n',78),
 ('parent mount only','2 1 8:1 /NAS /downloads-parent rw - ext4 /dev/sda rw\n',78),
 ('NAS bind','2 1 8:1 /NAS/Movies /downloads rw,relatime - ext4 /dev/sda rw\n',0),
 ('NAS path with spaces','2 1 8:1 /NAS/My\\040Movies /downloads rw - ext4 /dev/sda rw\n',0),
 ('read-only NAS bind','2 1 8:1 /NAS/Movies /downloads ro,relatime - ext4 /dev/sda rw\n',78),
 ('anonymous volume','2 1 8:1 /var/lib/docker/volumes/'+('a'*64)+'/_data /downloads rw - ext4 /dev/sda rw\n',78),
 ('named local volume','2 1 8:1 /NAS/docker/volumes/downloads/_data /downloads rw - ext4 /dev/sda rw\n',78),
]
for name,mountinfo,code in cases:
 r=subprocess.run(['awk','-f',str(root/'scripts/check-downloads.awk')],input=mountinfo,text=True,capture_output=True)
 assert r.returncode==code,(name,r.returncode,r.stderr)
 if code:assert '/downloads' in r.stderr,(name,r.stderr)
 print('PASS mount validation:',name)
