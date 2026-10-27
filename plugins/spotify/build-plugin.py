"""Run after build-runtime.sh. Does not access a speaker."""
from pathlib import Path
import hashlib,json,os,subprocess,zipfile
root=Path(__file__).resolve().parents[2]
plugin=Path(__file__).resolve().parent
runtime=plugin/'service/runtime'
if not (runtime/'lib/ld-linux-aarch64.so.1').is_file():raise SystemExit('run build-runtime.sh first')
manifest=json.loads((plugin/'manifest.json').read_text(encoding='utf-8'))
# Plugin data holds the unpacked runtime, two Soloist builds while one
# updates (about 35 MB each), the trimmed audio cache and Soloist's state.
bundled=sum(f.stat().st_size for f in runtime.rglob('*') if f.is_file())
manifest['runtime_storage_bytes']=bundled+2*(40<<20)+(24<<20)+(2<<20)
env=os.environ.copy();env.update(CGO_ENABLED='0',GOOS='linux',GOARCH='arm64')
subprocess.run(['go','build','-tags=spotify','-trimpath','-ldflags=-s -w','-o',str(plugin/'plugin.elf'),'./plugins/spotify/service'],cwd=root,env=env,check=True)
manifest['module_sha256']=hashlib.sha256((plugin/'plugin.elf').read_bytes()).hexdigest()
out=root/'release';out.mkdir(exist_ok=True)
target=out/f"spotify-connect-{manifest['version']}.aliceplugin"
# The manifest in the tree keeps its placeholders; only the package gets the
# computed size and digest.
with zipfile.ZipFile(target,'w',zipfile.ZIP_DEFLATED,compresslevel=9) as z:
 z.writestr('manifest.json',json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
 for name in ['plugin.elf','README.md','LICENSE']:z.write(plugin/name,name)
print(target,hashlib.sha256(target.read_bytes()).hexdigest())
