/* Page init of the moderngolf.co.id theme (inline script of every page). */
function copyToClipboard() {
            var sURL = window.location.href;
            sTemp = "<input id=\"copy_to_Clipboard\" value=\"" + sURL + "\" />"
            $("body").append(sTemp);
            $("#copy_to_Clipboard").select();
            document.execCommand("copy");
            $("#copy_to_Clipboard").remove();
            alert("Link copied");  
        }
        $(document).ready(function(){
            $(".hero-slider").owlCarousel({
                navigation: false,
                pagination: true,
                singleItem: true,
                autoPlay: true
            });
            $(".custom-slider").owlCarousel({
                navigation: false,
                pagination: true,
                items: 1.2,
                autoPlay: true
            });
            $(".side-slider").owlCarousel({
                navigation: false,
                pagination: true,
                singleItem: true,
                autoPlay: 3000
            });

            var sync1 = $("#sync1");
            var sync2 = $("#sync2");

            sync1.owlCarousel({
                singleItem : true,
                slideSpeed : 1000,
                navigation: true,
                pagination:false,
                afterAction : syncPosition,
                responsiveRefreshRate : 200,
            });

            sync2.owlCarousel({
                items : 4,
                itemsDesktop      : [1199,4],
                itemsDesktopSmall     : [979,4],
                itemsTablet       : [768,4],
                itemsMobile       : [479,3],
                pagination:false,
                responsiveRefreshRate : 100,
                afterInit : function(el){
                el.find(".owl-item").eq(0).addClass("synced");
                }
            });

            function syncPosition(el){
                var current = this.currentItem;
                $("#sync2")
                .find(".owl-item")
                .removeClass("synced")
                .eq(current)
                .addClass("synced")
                if($("#sync2").data("owlCarousel") !== undefined){
                center(current)
                }

            }

            $("#sync2").on("click", ".owl-item", function(e){
                e.preventDefault();
                var number = $(this).data("owlItem");
                sync1.trigger("owl.goTo",number);
            });

            function center(number){
                var sync2visible = sync2.data("owlCarousel").owl.visibleItems;

                var num = number;
                var found = false;
                for(var i in sync2visible){
                if(num === sync2visible[i]){
                    var found = true;
                }
                }

                if(found===false){
                if(num>sync2visible[sync2visible.length-1]){
                    sync2.trigger("owl.goTo", num - sync2visible.length+2)
                }else{
                    if(num - 1 === -1){
                    num = 0;
                    }
                    sync2.trigger("owl.goTo", num);
                }
                } else if(num === sync2visible[sync2visible.length-1]){
                sync2.trigger("owl.goTo", sync2visible[1])
                } else if(num === sync2visible[0]){
                sync2.trigger("owl.goTo", num-1)
                }
            }
        });
