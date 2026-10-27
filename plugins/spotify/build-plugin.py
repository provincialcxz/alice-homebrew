"""Run after build-receiver.sh. Does not access a speaker."""
from pathlib import Path
import hashlib,json,os,subprocess,zipfile
root=Path(__file__).resolve().parents[2]
plugin=Path(__file__).resolve().parent
receiver=plugin/'service/receiver-arm64'
manifest=json.loads((plugin/'manifest.json').read_text(encoding='utf-8'))
# The receiver is written to plugin data on start; the credential cache is small.
manifest['runtime_storage_bytes']=receiver.stat().st_size+(2<<20)
env=os.environ.copy();env.update(CGO_ENABLED='0',GOOS='linux',GOARCH='arm64')
subprocess.run(['go','build','-tags=spotify','-trimpath','-ldflags=-s -w','-o',str(plugin/'plugin.elf'),'./plugins/spotify/service'],cwd=root,env=env,check=True)
manifest['module_sha256']=hashlib.sha256((plugin/'plugin.elf').read_bytes()).hexdigest()
(plugin/'manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
out=root/'release';out.mkdir(exist_ok=True)
target=out/f"spotify-connect-{manifest['version']}.aliceplugin"
with zipfile.ZipFile(target,'w',zipfile.ZIP_DEFLATED,compresslevel=9) as z:
 for name in ['manifest.json','plugin.elf','README.md','LICENSE']:z.write(plugin/name,name)
print(target,hashlib.sha256(target.read_bytes()).hexdigest())
